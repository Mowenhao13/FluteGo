package alc

import (
	"Flute_go/pkg/lct"
	"Flute_go/pkg/object"
	"Flute_go/pkg/oti"
	"errors"
	"time"
)

// AlcPkt 表示一个 ALC 数据包（引用数据版本）
type AlcPkt struct {
	Lct                 lct.LCTHeader // LCT协议头
	Oti                 *oti.Oti      // 传输参数（FEC编码类型等），可选
	TransferLength      *uint64       // 传输数据总长度，可选
	Cenc                *lct.Cenc     // 内容编码（如Gzip），可选
	ServerTime          *time.Time    // 服务器时间（用于同步），可选
	Data                []byte        // 原始数据引用
	DataAlcHeaderOffset int           // ALC头偏移量
	DataPayloadOffset   int           // 有效载荷偏移量
	FdtInfo             *ExtFDT       // 文件描述表扩展信息，可选
}

// AlcPktCache 可缓存的数据包（持有数据所有权版本）
type AlcPktCache struct {
	Lct                 lct.LCTHeader // LCT协议头
	Oti                 *oti.Oti      // 传输参数（FEC编码类型等），可选
	TransferLength      *uint64       // 传输数据总长度，可选
	Cenc                *lct.Cenc     // 内容编码（如Gzip），可选
	ServerTime          *time.Time    // 服务器时间（用于同步），可选
	DataAlcHeaderOffset int           // ALC头偏移量
	DataPayloadOffset   int           // 有效载荷偏移量
	Data                []byte        // 数据所有权版本
	FdtInfo             *ExtFDT       // 文件描述表扩展信息，可选
}

// PayloadID Payload 标识符
type PayloadID struct {
	Sbn               uint32  // Source Block Number
	Esi               uint32  // Encoding Symbol Number
	SourceBlockLength *uint32 // Source Block Length，可选
}

// ExtFDT 文件描述表扩展信息
type ExtFDT struct {
	Version       uint32 // FDT 版本
	FdtInstanceID uint32 // FDT 实例 ID
}

type AlcCodec interface {
	AddFti(data *[]byte, oti oti.Oti, transferLength uint64)
	GetFti(data []byte, lctHeader lct.LCTHeader) (oti.Oti, uint64, error)
	AddFecPayloadId(data *[]byte, oti oti.Oti, pkt object.Pkt)
	GetFecPayloadId(pkt AlcPkt, oti oti.Oti) (PayloadID, error)
	GetFecInlinePayloadId(pkt AlcPkt) (PayloadID, error)
	FecPayloadIdBlockLength() uint
}

// noOpCodec 是一个空实现，用来占位，保证系统可跑
type noOpCodec struct{}

var (
	// 统一错误
	ErrNotRegistered  = errors.New("alc codec not registered")
	ErrNotImplemented = errors.New("alc codec method not implemented")

	// 注册表：FECEncodingID -> 实例（单例）
	registry = map[oti.FECEncodingID]AlcCodec{}

	// 兜底占位实现（NoOp/Stub），保证总能返回一个实现
	defaultNoOp = &noOpCodec{}
)

// 注册入口：各实现包在其 init() 里调用 Register
func Register(id oti.FECEncodingID, impl AlcCodec) {
	registry[id] = impl
}

// 推荐工厂：返回实现；没有就返回占位
func Instance(id oti.FECEncodingID) AlcCodec {
	if impl, ok := registry[id]; ok {
		return impl
	}
	// 没注册的都回落到 NoOp，以便系统继续工作（可观测）
	return defaultNoOp
}

func (n *noOpCodec) AddFti(data *[]byte, _ oti.Oti, _ uint64) {
	// 不修改 data，或者你也可以选择向 data 追加占位字段，视联调需求而定
}

func (n *noOpCodec) GetFti(_ []byte, _ lct.LCTHeader) (oti.Oti, uint64, error) {
	return oti.Oti{}, 0, ErrNotImplemented
}

func (n *noOpCodec) AddFecPayloadId(_ *[]byte, _ oti.Oti, _ object.Pkt) {
	// 不做事
}

func (n *noOpCodec) GetFecPayloadId(_ AlcPkt, _ oti.Oti) (PayloadID, error) {
	return PayloadID{}, ErrNotImplemented
}

func (n *noOpCodec) GetFecInlinePayloadId(_ AlcPkt) (PayloadID, error) {
	return PayloadID{}, ErrNotImplemented
}

func (n *noOpCodec) FecPayloadIdBlockLength() uint {
	return 0
}

// 默认把 NoCode 绑定到 noOp；等你有真实实现后在实现包里 Register 覆盖即可
func init() {
	Register(oti.NoCode, defaultNoOp)
}
