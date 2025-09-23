package sender

import (
	"Flute_go/pkg/object"
	"Flute_go/pkg/oti"
	"Flute_go/pkg/tools"
	t "Flute_go/pkg/type"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type FDTPublishMode int

const (
	FullFDT FDTPublishMode = iota
	ObjectsBeingTransferred
)

// TransferInfo
type TransferInfo struct {
	transferring           bool
	transferCount          uint32
	totalNbTransfer        uint64
	lastTransferEndTime    *time.Time
	lastTransferStartTime  *time.Time
	nextTransferTimestamp  *time.Time
	packetTransmissionTick *time.Duration
	transferStartTime      *time.Time
}

func (t *TransferInfo) init(obj *ObjectDesc, o *oti.Oti, now time.Time) {
	t.transferring = true
	t.lastTransferStartTime = &now

	var pktTick *time.Duration
	if obj.TargetAcquisition != nil {
		switch obj.TargetAcquisition.Choice {
		case AsFastAsPossible:
			// 不限速：pktTick 仍为 nil
		case WithinDuration:
			nbPackets := tools.DivCeil(obj.TransferLength, uint64(o.EncodingSymbolLength))
			if nbPackets > 0 {
				d := tools.DurationDivFloat(obj.TargetAcquisition.Duration, float64(nbPackets))
				pktTick = &d
			}
		case WithinTime:
			dur := obj.TargetAcquisition.At.Sub(now)
			if dur <= 0 {
				// 对齐原日志行为
				// log.Warnf(...)
				// 这里保持静默或用你项目的日志
			}
			nbPackets := tools.DivCeil(obj.TransferLength, uint64(o.EncodingSymbolLength))
			if nbPackets > 0 {
				d := tools.DurationDivFloat(dur, float64(nbPackets))
				pktTick = &d
			}
		}
	}

	t.packetTransmissionTick = pktTick
	if t.packetTransmissionTick != nil {
		// 下一次传输时间从 now 起步
		nt := now
		t.nextTransferTimestamp = &nt
	}

	// 轮播：若当前计数==最大次数且存在 carousel_mode，则清 0（进入新一轮）
	if obj.MaxTransferCount > 0 && obj.CarouselMode != nil {
		if t.transferCount == obj.MaxTransferCount {
			t.transferCount = 0
		}
	}
}

func (t *TransferInfo) done(now time.Time) {
	t.transferring = false
	t.transferCount++
	t.totalNbTransfer++
	t.lastTransferEndTime = &now
}

func (t *TransferInfo) tick() {
	if t.packetTransmissionTick == nil || t.nextTransferTimestamp == nil {
		return
	}
	next := t.nextTransferTimestamp.Add(*t.packetTransmissionTick)
	t.nextTransferTimestamp = &next
}

// FileDesc

type FileDesc struct {
	Priority          uint32
	Object            *ObjectDesc
	Oti               oti.Oti
	FdtID             *uint32
	SenderCurrentTime bool

	published    atomic.Bool
	TOI          t.Uint128
	mu           sync.RWMutex
	transferInfo TransferInfo
}

func NewFileDesc(
	priority uint32,
	obj *ObjectDesc,
	defaultOti *oti.Oti,
	fdtID *uint32,
	senderCurrentTime bool,
) (*FileDesc, error) {
	// TOI 校验：允许只判断 nil；是否为零值可按你 tools.Uint128 的实现追加 IsZero() 判定
	if obj.Toi == nil {
		return nil, errors.New("Object TOI is required")
	}

	// 选择对象级 OTI 或默认 OTI
	otiVal := *defaultOti
	//TODO: 选择对象级 OTI 或默认 OTI
	//if obj.OTIOverrideJSON != nil {
	//	// 可选：支持对象级 OTI 覆盖
	//	// _ = json.Unmarshal([]byte(*obj.OTIOverrideJSON), &otiVal)
	//}

	maxTransferLen := otiVal.MaxTransferLength()
	if obj.TransferLength > uint64(maxTransferLen) {
		return nil, errors.New(fmt.Sprintf(
			"Object transfer length of %d is bigger than %d, incompatible with OTI",
			obj.TransferLength, maxTransferLen,
		))
	}

	ti := TransferInfo{
		transferring:           false,
		transferCount:          0,
		totalNbTransfer:        0,
		nextTransferTimestamp:  nil,
		packetTransmissionTick: nil,
		transferStartTime:      obj.TransferStartTime,
	}

	fd := &FileDesc{
		Priority:          priority,
		Object:            obj,
		Oti:               otiVal,
		FdtID:             fdtID,
		SenderCurrentTime: senderCurrentTime,
		TOI:               obj.Toi.value, // ★ 直接存 Uint128
		transferInfo:      ti,
	}
	fd.published.Store(false)
	return fd, nil
}

func (f *FileDesc) TotalNbTransfer() uint64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.transferInfo.totalNbTransfer
}

func (f *FileDesc) CanTransferBeStopped() bool {
	if f.Object.AllowImmediateStopBeforeFirstTransfer != nil && *f.Object.AllowImmediateStopBeforeFirstTransfer {
		return true
	}
	return f.TotalNbTransfer() > 0
}

func (f *FileDesc) TransferStarted(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transferInfo.init(f.Object, &f.Oti, now)
}

func (f *FileDesc) TransferDone(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transferInfo.done(now)
}

func (f *FileDesc) IsExpired() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// 若还没达到最大次数，不过期
	if f.Object.MaxTransferCount > 0 && f.Object.MaxTransferCount > f.transferInfo.transferCount {
		return false
	}
	// 没有 carousel 模式，则过期；有 carousel 则不算过期
	return f.Object.CarouselMode == nil
}

func (f *FileDesc) IsTransferring() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.transferInfo.transferring
}

func (f *FileDesc) NextTransferTimestamp() (time.Time, bool) {
	if f.transferInfo.nextTransferTimestamp == nil {
		return time.Time{}, false
	}
	return *f.transferInfo.nextTransferTimestamp, true
}

func (f *FileDesc) IncNextTransferTimestamp() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transferInfo.tick()
}

func (f *FileDesc) ResetLastTransfer(startTime *time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transferInfo.lastTransferEndTime = nil
	f.transferInfo.lastTransferStartTime = nil
	if startTime != nil {
		f.transferInfo.transferStartTime = startTime
	}
}

func (f *FileDesc) IsLastTransfer() bool {
	// 有 carousel 则永远不是“最后一次”
	if f.Object.CarouselMode != nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.Object.MaxTransferCount > 0 {
		return false
	}
	return f.Object.MaxTransferCount == f.transferInfo.transferCount+1
}

func (f *FileDesc) ShouldTransferNow(priority uint32, mode FDTPublishMode, now time.Time) bool {
	if f.Priority != priority {
		return false
	}
	if mode == FullFDT && !f.IsPublished() {
		// log.Warnf("File with TOI %s is not published", f.TOI)
		return false
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	// 未到开始时间
	if f.transferInfo.transferStartTime != nil && now.Before(*f.transferInfo.transferStartTime) {
		return false
	}
	// 正在传输
	if f.transferInfo.transferring {
		return false
	}

	// 还没达到最大次数：立刻可以传
	if f.Object.MaxTransferCount > 0 && f.Object.MaxTransferCount > f.transferInfo.transferCount {
		return true
	}

	// 没有轮播 || 上次时间缺失：允许传
	if f.Object.CarouselMode == nil ||
		f.transferInfo.lastTransferEndTime == nil ||
		f.transferInfo.lastTransferStartTime == nil {
		return true
	}

	// 轮播策略
	cm := f.Object.CarouselMode
	var last time.Time
	var interval time.Duration
	switch cm.Choice {
	case DelayBetweenTransfers:
		last = *f.transferInfo.lastTransferEndTime
		interval = cm.Interval
	case IntervalBetweenStartTimes:
		last = *f.transferInfo.lastTransferStartTime
		interval = cm.Interval
	}
	lastInterval := now.Sub(last)
	return lastInterval > interval
}

func (f *FileDesc) IsPublished() bool {
	return f.published.Load()
}

func (f *FileDesc) SetPublished() {
	f.published.Store(true)
}

// ToFileXML 等价 to_file_xml
func (f *FileDesc) ToFileXML(now time.Time) object.FdtFile {
	// 从 OTI 生成 FDT 所需属性（注意：OtiAttributes 使用 FecOti* 命名）
	attr := f.Oti.GetAttributes()

	// Cache-Control（严格按 object 包定义）
	var cc *object.CacheControl
	if f.Object.CacheControl != nil {
		var choice object.CacheControlChoice

		switch f.Object.CacheControl.Choice {
		case CacheNoCache:
			b := true
			choice.NoCache = &b

		case CacheMaxStale:
			b := true
			choice.MaxStale = &b

		case CacheExpires:
			// 相对过期时长 -> 秒
			secs := f.Object.CacheControl.Duration
			if secs < 0 {
				secs = 0
			}
			s := uint32(secs / time.Second)
			choice.Expires = &s

		case CacheExpireAt:
			// 绝对过期时间 -> 距 now 的剩余秒
			at := f.Object.CacheControl.At
			var rem uint32
			if at.After(now) {
				rem = uint32(at.Sub(now) / time.Second)
			} else {
				rem = 0
			}
			choice.Expires = &rem
		}

		cc = &object.CacheControl{Value: choice}
	}

	return object.FdtFile{
		// 标识
		ContentLocation: f.Object.ContentLocation.String(),
		TOI:             f.TOI.String(),

		// 长度
		ContentLength:  &f.Object.ContentLength,  // *uint64
		TransferLength: &f.Object.TransferLength, // *uint64

		// 内容类型
		ContentType:     &f.Object.ContentType,
		ContentEncoding: tools.StrPtr(f.Object.Cenc.String()),
		ContentMD5:      f.Object.MD5,

		// 文件级 FEC OTI：把 FecOti* 映射到 FEC*（字段类型也匹配 *uint8/*uint64/*string）
		FECEncID:      attr.FecOtiFecEncodingID,
		FECInstanceID: attr.FecOtiFecInstanceID,
		FECMaxSBL:     attr.FecOtiMaximumSourceBlockLength,
		FECESL:        attr.FecOtiEncodingSymbolLength,
		FECMaxN:       attr.FecOtiMaxNumberOfEncodingSymbols,
		FECSchemeInfo: attr.FecOtiSchemeSpecificInfo,

		// 子元素
		CacheControl: cc,
	}
}
