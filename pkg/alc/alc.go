package alc

import (
	"Flute_go/pkg/lct"
	"Flute_go/pkg/object"
	"Flute_go/pkg/oti"
	"Flute_go/pkg/profile"
	"Flute_go/pkg/tools"
	t "Flute_go/pkg/type"
	"errors"
	"fmt"
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

// NewAlcPktCloseSession 生成 Close-Session ALC 包
func NewAlcPktCloseSession(cci *t.Uint128, tsi uint64) []byte {
	buf := make([]byte, 0, 64)

	// 按原实现，CloseSession 用 no_code（FECP=NoCode）
	otiNoCode := oti.NewNoCode(0, 0)

	// psi=0, closeObject=false, closeSession=true
	lct.PushLCTHeader(&buf, 0, *cci, tsi, t.Uint128{}, uint8(otiNoCode.FecEncodingID), false, true)

	// 加 FTI（0 长度）
	Instance(otiNoCode.FecEncodingID).AddFti(&buf, *otiNoCode, 0)

	// Add FEC Payload ID（按 RFC：CloseSession 也放个0占位）
	buf = append(buf, 0, 0, 0, 0)

	return buf
}

// NewAlcPkt 把 pkt 封成 ALC/LCT 原始字节
func NewAlcPkt(
	o *oti.Oti,
	cci t.Uint128,
	tsi uint64,
	p *object.Pkt,
	prof profile.Profile,
	now time.Time,
) []byte {
	buf := make([]byte, 0, len(p.Payload)+64)

	// 1) LCT 头（psi=0）
	lct.PushLCTHeader(&buf, 0, cci, tsi, p.Toi, uint8(o.FecEncodingID), p.CloseObject, false)

	// 2) FDT 扩展（仅 FDT 包）
	if p.Toi == lct.TOI_FDT {
		var version uint8
		switch prof {
		case profile.RFC6726:
			version = 2
		case profile.RFC3926:
			version = 1
		default:
			version = 2
		}
		if p.FdtID != nil {
			pushFDT(&buf, version, *p.FdtID)
		}
	}

	// 3) CENC 扩展（FDT 且非 Null，或者 inband_cenc）
	if (p.Toi == lct.TOI_FDT && p.Cenc != lct.CencNull) || p.InbandCenc {
		c := uint8(lct.CencNull)
		pushCenc(&buf, c)
	}

	// 4) Sender Current Time
	if p.SenderCurrentTime {
		pushSCT(&buf, now)
	}

	// 5) FTI + FEC Payload ID
	codec := Instance(o.FecEncodingID)
	if p.Toi == lct.TOI_FDT || o.InBandFti {
		tlen := uint64(0)
		if p.TransferLength > 0 {
			tlen = p.TransferLength
		}
		codec.AddFti(&buf, *o, tlen)
	}
	codec.AddFecPayloadId(&buf, *o, *p)

	// 6) Payload
	pushPayload(&buf, p)

	return buf
}

// ParseAlcPkt 解析 ALC 包
func ParseAlcPkt(data []byte) (*AlcPkt, error) {
	// LCT
	hdr, err := lct.ParseLCTHeader(data)
	if err != nil {
		return nil, err
	}

	fecID, err := oti.FECEncodingIDFromByte(hdr.Cp) // 你可以实现这个：把 cp->FECEncodingID
	if err != nil {
		return nil, err
	}

	codec := Instance(fecID)
	fecPIDLen := codec.FecPayloadIdBlockLength()
	if int(fecPIDLen)+int(hdr.Len) > len(data) {
		return nil, fmt.Errorf("wrong ALC size: fecPIDLen=%d, lctLen=%d, dataLen=%d",
			fecPIDLen, hdr.Len, len(data))
	}

	// FTI
	otiVal, transferLen, _ := codec.GetFti(data, *hdr)
	var otiPtr *oti.Oti
	var tlPtr *uint64
	if otiVal.FecEncodingID != 0 || transferLen > 0 {
		otiPtr = &otiVal
		tlPtr = &transferLen
	}

	alcHeaderOffset := hdr.Len
	payloadOffset := int(fecPIDLen) + int(hdr.Len)

	// CENC
	var cencPtr *lct.Cenc
	if ext, err := lct.GetExt(data, hdr, uint8(lct.ExtCenc)); err == nil {
		if c, err := parseCenc(ext); err == nil {
			cencPtr = &c
		}
	}

	// FDT info (仅当 TOI==FDT)
	var fdtInfo *ExtFDT
	if hdr.Toi == lct.TOI_FDT {
		if ext, err := lct.GetExt(data, hdr, uint8(lct.ExtFdt)); err == nil {
			if info, err := parseExtFDT(ext); err == nil && info != nil {
				fdtInfo = info
			}
		}
	}

	return &AlcPkt{
		Lct:                 *hdr,
		Oti:                 otiPtr,
		TransferLength:      tlPtr,
		Cenc:                cencPtr,
		ServerTime:          nil,
		Data:                data,
		DataAlcHeaderOffset: int(alcHeaderOffset),
		DataPayloadOffset:   payloadOffset,
		FdtInfo:             fdtInfo,
	}, nil
}

// GetSenderCurrentTime 解析 EXT_TIME
func GetSenderCurrentTime(pkt *AlcPkt) (*time.Time, error) {
	ext, err := lct.GetExt(pkt.Data, &pkt.Lct, uint8(lct.ExtTime))
	if err != nil {
		return nil, err
	}
	tm, err := parseSCT(ext)
	if err != nil {
		return nil, err
	}
	if tm == nil {
		return nil, nil
	}
	return tm, nil
}

// ParsePayloadID 使用 codec 从包中解析 PayloadID
func ParsePayloadID(pkt *AlcPkt, o *oti.Oti) (*PayloadID, error) {
	pl, err := Instance(o.FecEncodingID).GetFecPayloadId(*pkt, *o)
	if err != nil {
		return nil, err
	}
	return &PayloadID{Sbn: pl.Sbn, Esi: pl.Esi, SourceBlockLength: pl.SourceBlockLength}, nil
}

// GetFecInlinePayloadId 解析 inline FEC Payload Id
func GetFecInlinePayloadId(pkt *AlcPkt) (*PayloadID, error) {
	fecID, err := oti.FECEncodingIDFromByte(pkt.Lct.Cp)
	if err != nil {
		return nil, err
	}
	pl, err := Instance(fecID).GetFecInlinePayloadId(*pkt)
	if err != nil {
		return nil, err
	}
	return &PayloadID{Sbn: pl.Sbn, Esi: pl.Esi, SourceBlockLength: pl.SourceBlockLength}, nil
}

// ---------------- helpers ----------------

func pushFDT(buf *[]byte, version uint8, fdtID uint32) {
	// (HET=192)<<24 | (V)<<20 | FDT Instance ID(20bit)
	ext := (uint32(lct.ExtFdt) << 24) | (uint32(version) << 20) | (fdtID & 0xFFFFF)
	*buf = append(*buf, byte(ext>>24), byte(ext>>16), byte(ext>>8), byte(ext))
	lct.IncHdrLen(*buf, 1)
}

func pushCenc(buf *[]byte, cenc uint8) {
	// HET=193, Cenc in bits[15:8]
	ext := (uint32(lct.ExtCenc) << 24) | (uint32(cenc) << 16)
	*buf = append(*buf, byte(ext>>24), byte(ext>>16), byte(ext>>8), byte(ext))
	lct.IncHdrLen(*buf, 1)
}

func parseCenc(ext []byte) (lct.Cenc, error) {
	if len(ext) != 4 {
		return lct.CencNull, fmt.Errorf("wrong CENC ext len")
	}
	val := ext[1]
	switch lct.Cenc(val) {
	case lct.CencNull, lct.CencZlib, lct.CencDeflate, lct.CencGzip:
		return lct.Cenc(val), nil
	default:
		return lct.CencNull, fmt.Errorf("unsupported Cenc=%d", val)
	}
}

func pushSCT(buf *[]byte, tm time.Time) {
	// HET=2, HEL=3, Use: SCT_hi=1, SCT_low=1
	header := (uint32(lct.ExtTime) << 24) | (3 << 16) | (1 << 15) | (1 << 14)

	ntp, err := tools.SystemTimeToNTP(tm)
	if err != nil {
		return
	}
	*buf = append(*buf, byte(header>>24), byte(header>>16), byte(header>>8), byte(header))
	*buf = append(*buf, byte(ntp>>56), byte(ntp>>48), byte(ntp>>40), byte(ntp>>32)) // seconds (hi 32)
	*buf = append(*buf, byte(ntp>>24), byte(ntp>>16), byte(ntp>>8), byte(ntp))      // fraction (low 32)
	lct.IncHdrLen(*buf, 3)
}

func parseSCT(ext []byte) (*time.Time, error) {
	if len(ext) < 4 {
		return nil, fmt.Errorf("sct too short")
	}
	useBitsHi := ext[2]
	sctHi := (useBitsHi >> 7) & 1
	sctLo := (useBitsHi >> 6) & 1
	ert := (useBitsHi >> 5) & 1
	slc := (useBitsHi >> 4) & 1

	expected := int((sctHi + sctLo + ert + slc + 1) * 4)
	if len(ext) != expected {
		return nil, fmt.Errorf("wrong sct length: expect=%d, got=%d", expected, len(ext))
	}
	if sctHi == 0 {
		return nil, nil
	}

	sec := uint32(ext[4])<<24 | uint32(ext[5])<<16 | uint32(ext[6])<<8 | uint32(ext[7])
	fra := uint32(0)
	if sctLo == 1 && len(ext) >= 12 {
		fra = uint32(ext[8])<<24 | uint32(ext[9])<<16 | uint32(ext[10])<<8 | uint32(ext[11])
	}
	ntp := (uint64(sec) << 32) | uint64(fra)
	tm, err := tools.NTPToSystemTime(ntp)
	if err != nil {
		return nil, err
	}
	return &tm, nil
}

func parseExtFDT(ext []byte) (*ExtFDT, error) {
	if len(ext) != 4 {
		return nil, fmt.Errorf("wrong FDT ext len")
	}
	val := uint32(ext[0])<<24 | uint32(ext[1])<<16 | uint32(ext[2])<<8 | uint32(ext[3])
	version := (val >> 20) & 0xF
	instanceID := val & 0xFFFFF
	return &ExtFDT{
		Version:       uint32(version),
		FdtInstanceID: instanceID,
	}, nil
}

func pushPayload(buf *[]byte, p *object.Pkt) {
	*buf = append(*buf, p.Payload...)
}
