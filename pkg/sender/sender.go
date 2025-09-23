package sender

import (
	"Flute_go/pkg/alc"
	"Flute_go/pkg/lct"
	"Flute_go/pkg/oti"
	"Flute_go/pkg/profile"
	"Flute_go/pkg/transport"
	t "Flute_go/pkg/type"
	"errors"
	"fmt"
	"sort"
	"time"
)

type PriorityQueue struct {
	// 在该优先级队列内并行/交错传输的文件个数上限
	// 0 或 1 表示串行；>=2 表示在窗口里多文件交错
	MultiplexFiles uint32
}

const (
	// PQHighest / Associated constant representing the highest priority level
	PQHighest uint32 = 0
	// PQHigh / Associated constant representing a high priority level
	PQHigh uint32 = 1
	// PQMedium / Associated constant representing a medium priority level
	PQMedium uint32 = 2
	// PQLow / Associated constant representing a low priority level
	PQLow uint32 = 3
	// PQVeryLow / Associated constant representing a very low priority level
	PQVeryLow uint32 = 4
)

func NewPriorityQueue(multiplex uint32) PriorityQueue {
	return PriorityQueue{MultiplexFiles: multiplex}
}

type Config struct {
	// FDT 生存时长（过期判断使用）
	FDTDuration time.Duration
	// FDT 轮播策略（与 Rust: CarouselRepeatMode 对齐）
	FDTCarouselMode CarouselRepeatMode
	// FDT 起始 ID
	FDTStartID uint32
	// FDT 的内容编码
	FDTCenc lct.Cenc
	// FDT 包是否在 LCT/ALC 中携带 SCT
	FDTInbandSCT bool
	// FDT 发布模式
	FDTPublishMode FDTPublishMode

	// 优先级队列配置：key 越小优先级越高
	PriorityQueues map[uint32]PriorityQueue

	// 单文件传输时，互相交错的源块窗口大小
	InterleaveBlocks uint8

	// 发送 Profile
	Profile profile.Profile

	// TOI 最大位数限制
	TOIMaxLength TOIMaxLength
	// TOI 初始值（nil = 随机）
	TOIInitialValue *t.Uint128 // 你若已有 t.Uint128/自定义类型，替换为你的类型

	// FDT-Instance 的 group 列表
	Groups []string
}

func DefaultConfig() Config {
	return Config{
		FDTDuration:     time.Hour,
		FDTCarouselMode: CarouselRepeatMode{Choice: DelayBetweenTransfers, Interval: time.Second},
		FDTStartID:      1,
		FDTCenc:         lct.CencNull,
		FDTInbandSCT:    true,
		FDTPublishMode:  FullFDT,

		PriorityQueues: map[uint32]PriorityQueue{
			0: {MultiplexFiles: 3},
		},
		InterleaveBlocks: 4,
		Profile:          profile.RFC6726,
		TOIMaxLength:     ToiMax112,
		TOIInitialValue:  &t.Uint128{High: 0, Low: 1},
		Groups:           nil,
	}
}

func (c *Config) SetPriorityQueue(priority uint32, pq PriorityQueue) {
	if c.PriorityQueues == nil {
		c.PriorityQueues = make(map[uint32]PriorityQueue)
	}
	c.PriorityQueues[priority] = pq
}

func (c *Config) RemovePriorityQueue(priority uint32) {
	delete(c.PriorityQueues, priority)
}

type senderSessionList struct {
	index    int
	sessions []*SenderSession // 指针切片
}

type Sender struct {
	fdt         *Fdt           // 指针
	fdtSession  *SenderSession // 指针
	sessions    map[uint32]*senderSessionList
	observers   *ObserverList // 指针
	tsi         uint64
	udpEndpoint transport.UDPEndpoint
}

func NewSender(endpoint transport.UDPEndpoint, tsi uint64, o *oti.Oti, cfg *Config) *Sender {
	if cfg == nil {
		def := DefaultConfig()
		cfg = &def
	}

	observers := NewObserverList()

	fdt := NewFdt(
		tsi,
		cfg.FDTStartID,
		o,
		cfg.FDTCenc,
		cfg.FDTDuration,
		cfg.FDTCarouselMode,
		cfg.FDTInbandSCT,
		observers,
		cfg.TOIMaxLength,
		cfg.TOIInitialValue,
		&cfg.Groups,
		cfg.FDTPublishMode,
	)

	fdtSession := NewSenderSession(
		0,
		tsi,
		int(cfg.InterleaveBlocks),
		true, // transfer_fdt_only
		cfg.Profile,
		endpoint,
	)

	// 构建优先级队列的会话列表
	sessions := make(map[uint32]*senderSessionList, len(cfg.PriorityQueues))
	for prio, pq := range cfg.PriorityQueues {
		m := pq.MultiplexFiles
		if m == 0 {
			m = 1
		}
		list := &senderSessionList{
			index:    0,
			sessions: make([]*SenderSession, 0, m),
		}
		for i := uint32(0); i < m; i++ {
			ss := NewSenderSession(
				prio,
				tsi,
				int(cfg.InterleaveBlocks),
				false, // transfer_fdt_only
				cfg.Profile,
				endpoint,
			)
			list.sessions = append(list.sessions, ss)
		}
		sessions[prio] = list
	}

	return &Sender{
		fdt:         fdt,
		fdtSession:  fdtSession,
		sessions:    sessions,
		observers:   observers,
		tsi:         tsi,
		udpEndpoint: endpoint,
	}
}

// Subscribe / Unsubscribe
func (s *Sender) Subscribe(sub Subscriber) {
	s.observers.Subscribe(sub)
}

func (s *Sender) Unsubscribe(sub Subscriber) {
	s.observers.Unsubscribe(sub)
}

func (s *Sender) GetUDPEndpoint() *transport.UDPEndpoint {
	return &s.udpEndpoint
}

func (s *Sender) GetTSI() uint64 {
	return s.tsi
}

func (s *Sender) AddObject(priority uint32, obj *ObjectDesc) (uint128 t.Uint128, err error) {
	if _, ok := s.sessions[priority]; !ok {
		return t.Uint128{}, errors.New(fmt.Sprintf("priority queue %d does not exist", priority))
	}
	toi, e := s.fdt.AddObject(priority, obj)
	if e != nil {
		return t.Uint128{
			High: 0,
			Low:  0,
		}, e
	}
	return t.StringToUint128(toi), nil
}

func (s *Sender) TriggerTransferAt(toi t.Uint128, ts *time.Time) bool {
	var when *time.Time
	if ts != nil {
		t2 := *ts
		when = &t2
	}
	return s.fdt.TriggerTransferAt(toi.String(), when)
}

func (s *Sender) IsAdded(toi t.Uint128) bool {
	return s.fdt.IsAdded(toi.String())
}

func (s *Sender) RemoveObject(toi t.Uint128) bool {
	return s.fdt.RemoveObject(toi.String())
}

func (s *Sender) NbTransfers(toi t.Uint128) *uint64 {
	num, _ := s.fdt.NbTransfers(toi.String())
	return &num
}

func (s *Sender) NbObjects() int {
	return s.fdt.NbObjects()
}

func (s *Sender) Publish(now time.Time) error {
	return s.fdt.Publish(now)
}

func (s *Sender) SetComplete() {
	s.fdt.SetComplete()
}

func (s *Sender) ReadCloseSession(_ time.Time) []byte {
	// CCI = 0 (按原实现)
	return alc.NewAlcPktCloseSession(&t.Uint128{}, s.tsi)
}

func (s *Sender) AllocateToi() *Toi {
	return s.fdt.AllocateToi()
}

func (s *Sender) FdtXMLData(now time.Time) ([]byte, error) {
	return s.fdt.ToXML(now)
}

func (s *Sender) GetObjectsInFDT() map[string]*ObjectDesc {
	return s.fdt.GetObjectsInFDT()
}

func (s *Sender) Read(now time.Time) []byte {
	// 先让 fdtSession 尝试产生 FDT 包
	if data := s.fdtSession.Run(s.fdt, now); data != nil {
		return data
	}

	// 轮询优先级队列（按照优先级从小到大）
	if len(s.sessions) > 0 {
		// 为了固定顺序，收集并排序 key
		keys := make([]uint32, 0, len(s.sessions))
		for k := range s.sessions {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

		for _, prio := range keys {
			if data := s.readPriorityQueue(s.fdt, s.sessions[prio], now); data != nil {
				return data
			}
		}
	}

	// 再次尝试 FDT（与 Rust 一致）
	if data := s.fdtSession.Run(s.fdt, now); data != nil {
		return data
	}

	return nil
}

func (s *Sender) readPriorityQueue(fdt *Fdt, list *senderSessionList, now time.Time) []byte {
	if list == nil || len(list.sessions) == 0 {
		return nil
	}

	start := list.index
	for {
		sess := list.sessions[list.index] // 指针
		data := sess.Run(fdt, now)

		list.index++
		if list.index == len(list.sessions) {
			list.index = 0
		}

		if data != nil {
			return data
		}

		if list.index == start {
			break
		}
	}
	return nil
}
