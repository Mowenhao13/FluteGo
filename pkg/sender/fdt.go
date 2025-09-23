package sender

import (
	"Flute_go/pkg/lct"
	"Flute_go/pkg/object"
	"Flute_go/pkg/oti"
	"Flute_go/pkg/tools"
	t "Flute_go/pkg/type"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"time"
)

type Fdt struct {
	tsi                uint64
	fdtID              uint32
	oti                oti.Oti
	filesTransferQueue []*FileDesc // VecDeque<Arc<FileDesc>>
	fdtTransferQueue   []*FileDesc
	files              map[string]*FileDesc // HashMap<u128, Arc<FileDesc>> —— 用一个包装 key
	currentFdtTransfer *FileDesc
	complete           *bool

	cenc         lct.Cenc
	duration     time.Duration
	carouselMode CarouselRepeatMode
	inbandSCT    bool
	lastPublish  *time.Time

	observers *ObserverList
	// group 聚合（Rust: Option<Vec<String>>）
	groups *[]string

	toiAllocator *ToiAllocator
	publishMode  FDTPublishMode
}

func NewFdt(
	tsi uint64,
	fdtID uint32,
	defaultOti *oti.Oti,
	cenc lct.Cenc,
	duration time.Duration,
	carouselMode CarouselRepeatMode,
	inbandSCT bool,
	observers *ObserverList,
	toiMaxLength TOIMaxLength,
	toiInitialValue *t.Uint128,
	groups *[]string,
	publishMode FDTPublishMode,
) *Fdt {
	return &Fdt{
		tsi:                tsi,
		fdtID:              fdtID,
		oti:                *defaultOti,
		filesTransferQueue: make([]*FileDesc, 0),
		fdtTransferQueue:   make([]*FileDesc, 0),
		files:              make(map[string]*FileDesc),
		currentFdtTransfer: nil,
		complete:           nil,
		cenc:               cenc,
		duration:           duration,
		carouselMode:       carouselMode,
		inbandSCT:          inbandSCT,
		lastPublish:        nil,
		observers:          observers,
		groups:             groups,
		toiAllocator:       NewToiAllocator(toiMaxLength, toiInitialValue),
		publishMode:        publishMode,
	}
}

// 构建 FDT-Instance

func (f *Fdt) getFdtInstance(now time.Time) (*object.FdtInstance, error) {
	ntp, _ := tools.SystemTimeToNTP(now) // 失败就当 0
	expiresNTP := (ntp >> 32) + uint64(f.duration.Seconds())

	// Rust: 顶层 OTI 在 RaptorQ 时为 None；你已经去掉 RaptorQ，这里直接给顶层 OTI
	attr := f.oti.GetAttributes()

	// 选文件集合
	var list []*FileDesc
	switch f.publishMode {
	case ObjectsBeingTransferred:
		list = make([]*FileDesc, 0)
		for _, fd := range f.files {
			if fd.IsTransferring() {
				list = append(list, fd)
			}
		}
	default: // FullFDT
		list = make([]*FileDesc, 0, len(f.files))
		for _, fd := range f.files {
			list = append(list, fd)
		}
	}
	// 为了稳定性，按 TOI 排一下（可选）
	sort.Slice(list, func(i, j int) bool { return list[i].TOI.Less(list[j].TOI) })

	// 逐个文件转 FdtFile
	files := make([]object.FdtFile, 0, len(list))
	for _, fd := range list {
		files = append(files, fd.ToFileXML(now))
	}

	fullFDT := (*bool)(nil)
	if f.publishMode == FullFDT {
		v := true
		fullFDT = &v
	}

	inst := &object.FdtInstance{
		Expires:         fmt.Sprintf("%d", expiresNTP),
		Complete:        f.complete,
		ContentType:     nil,
		ContentEncoding: nil,

		// 顶层 FEC OTI（注意使用 object.FdtInstance 定义的字段名）
		FECEncID:      attr.FecOtiFecEncodingID,
		FECInstanceID: attr.FecOtiFecInstanceID,
		FECMaxSBL:     attr.FecOtiMaximumSourceBlockLength,
		FECESL:        attr.FecOtiEncodingSymbolLength,
		FECMaxN:       attr.FecOtiMaxNumberOfEncodingSymbols,
		FECSchemeInfo: attr.FecOtiSchemeSpecificInfo,

		Files: files,

		SchemaVersion: tools.Uint32Ptr(4),
		Group:         tools.PtrSliceToSlice(f.groups),
		// Base-URL-1/2、命名空间等如有需要再补
	}

	// group 列表（FdtInstance 这部分如果你结构里有，对齐填）
	_ = fullFDT // 如果你的 object.FdtInstance 有 FullFDT 字段，可设置
	return inst, nil
}

func (f *Fdt) GetFilesBeingTransferred() []*FileDesc {
	out := make([]*FileDesc, 0)
	for _, fd := range f.files {
		if fd.IsTransferring() {
			out = append(out, fd)
		}
	}
	return out
}

func (f *Fdt) AllocateToi() *Toi {
	return f.toiAllocator.Allocate()
}

func (f *Fdt) AddObject(priority uint32, obj *ObjectDesc) (string, error) {
	if f.complete != nil && *f.complete {
		return "", errors.New("FDT is complete, no new object should be added")
	}
	if obj.Toi == nil || obj.Toi.String() == "" {
		obj.SetToi(f.AllocateToi())
	}
	fd, err := NewFileDesc(priority, obj, &f.oti, nil, false)
	if err != nil {
		return "", err
	}
	toi := fd.TOI
	if _, dup := f.files[toi.String()]; dup {
		return "", errors.New("duplicate TOI in FDT")
	}
	f.files[toi.String()] = fd
	f.filesTransferQueue = append(f.filesTransferQueue, fd)
	return toi.String(), nil
}

func (f *Fdt) TriggerTransferAt(toi string, ts *time.Time) bool {
	fd, ok := f.files[toi]
	if !ok {
		return false
	}
	if fd.IsTransferring() {
		return true
	}
	fd.ResetLastTransfer(ts)
	return true
}

func (f *Fdt) GetObjectsInFDT() map[string]*ObjectDesc {
	out := make(map[string]*ObjectDesc, len(f.files))
	for k, v := range f.files {
		out[k] = v.Object
	}
	return out
}

func (f *Fdt) IsAdded(toi string) bool {
	_, ok := f.files[toi]
	return ok
}

func (f *Fdt) RemoveObject(toi string) bool {
	if _, ok := f.files[toi]; !ok {
		return false
	}
	delete(f.files, toi)
	dst := f.filesTransferQueue[:0]
	for _, fd := range f.filesTransferQueue {
		if fd.TOI.String() != toi {
			dst = append(dst, fd)
		}
	}
	f.filesTransferQueue = dst
	return true
}

func (f *Fdt) NbTransfers(toi string) (uint64, bool) {
	fd, ok := f.files[toi]
	if !ok {
		return 0, false
	}
	return fd.TotalNbTransfer(), true
}

func (f *Fdt) NbObjects() int {
	// 保留 Rust 的大对象日志逻辑：如需打印 URI 可在此处补
	return len(f.files)
}

func (f *Fdt) Publish(now time.Time) error {
	buf, err := f.ToXML(now)
	if err != nil {
		return err
	}

	obj, err := CreateFromBuffer(
		buf,
		"text/xml",
		mustParseURL("file:///"),
		1,
		&f.carouselMode,
		nil,
		nil,
		tools.PtrSliceToSlice(f.groups),
		f.cenc,
		true, // inband cenc
		nil,  // target acquisition
		true, // sender_current_time
	)
	if err != nil {
		return err
	}
	obj.Toi = f.toiAllocator.AllocateToiFDT()

	fd, err := NewFileDesc(0, obj, &f.oti, tools.Uint32Ptr(f.fdtID), f.inbandSCT)
	if err != nil {
		return err
	}
	fd.SetPublished()
	f.fdtTransferQueue = append(f.fdtTransferQueue, fd)

	f.fdtID = (f.fdtID + 1) & 0xFFFFF
	nowCopy := now
	f.lastPublish = &nowCopy

	for _, it := range f.files {
		it.SetPublished()
	}
	return nil
}

func (f *Fdt) NeedTransferFDT() bool {
	return len(f.fdtTransferQueue) > 0
}

func (f *Fdt) currentFdtWillExpire(now time.Time) bool {
	if len(f.fdtTransferQueue) > 0 {
		return false
	}
	if f.currentFdtTransfer == nil || f.lastPublish == nil {
		return true
	}
	d := now.Sub(*f.lastPublish)
	if f.duration > 30*time.Second {
		return f.duration+5*time.Second < d
	}
	return f.duration <= d
}

func (f *Fdt) GetNextFdtTransfer(now time.Time) *FileDesc {
	if f.currentFdtTransfer != nil && f.currentFdtTransfer.IsTransferring() {
		return nil
	}
	if f.currentFdtWillExpire(now) {
		_ = f.Publish(now)
	}
	if len(f.fdtTransferQueue) > 0 {
		f.currentFdtTransfer = f.fdtTransferQueue[0]
		f.fdtTransferQueue = f.fdtTransferQueue[1:]
	}
	if f.currentFdtTransfer == nil {
		return nil
	}
	if !f.currentFdtTransfer.ShouldTransferNow(0, f.publishMode, now) {
		return nil
	}
	f.currentFdtTransfer.TransferStarted(now)
	return f.currentFdtTransfer
}

func (f *Fdt) GetNextFileTransfer(priority uint32, now time.Time) *FileDesc {
	idx := -1
	for i, fd := range f.filesTransferQueue {
		if fd.ShouldTransferNow(priority, f.publishMode, now) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	fd := f.filesTransferQueue[idx]
	// 移除该元素
	copy(f.filesTransferQueue[idx:], f.filesTransferQueue[idx+1:])
	f.filesTransferQueue = f.filesTransferQueue[:len(f.filesTransferQueue)-1]

	// 事件：开始传输
	f.observers.Dispatch(Event{
		Kind: EventStartTransfer,
		File: FileInfo{Toi: fd.TOI.String()},
	}, now)

	fd.TransferStarted(now)

	if f.publishMode == ObjectsBeingTransferred {
		_ = f.Publish(now)
	}
	return fd
}

func (f *Fdt) TransferDone(fd *FileDesc, now time.Time) {
	fd.TransferDone(now)

	if fd.TOI == lct.TOI_FDT {
		if fd.IsExpired() {
			f.currentFdtTransfer = nil
		}
		return
	}

	// 普通文件的 stop 事件
	f.observers.Dispatch(Event{
		Kind: EventStopTransfer,
		File: FileInfo{Toi: fd.TOI.String()},
	}, now)

	if _, ok := f.files[fd.TOI.String()]; !ok {
		// 已被移除
		return
	}
	if !fd.IsExpired() {
		// 继续轮播
		f.filesTransferQueue = append(f.filesTransferQueue, fd)
		return
	}
	// 过期则从 FDT 中移除
	delete(f.files, fd.TOI.String())
	// 可选：自动 publish
	// _ = f.Publish(now)
}

func (f *Fdt) SetComplete() {
	v := true
	f.complete = &v
}

// ToXML 等价 Rust: to_xml()
func (f *Fdt) ToXML(now time.Time) ([]byte, error) {
	inst, err := f.getFdtInstance(now)
	if err != nil {
		return nil, err
	}
	out, err := xml.MarshalIndent(inst, "", "  ")
	if err != nil {
		return nil, err
	}
	// 带 XML 声明
	var buf bytes.Buffer
	buf.WriteString(xml.Header) // <?xml version="1.0" encoding="UTF-8"?>
	buf.Write(out)
	return buf.Bytes(), nil
}

// ------- 小工具 -------

func mustParseURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}
