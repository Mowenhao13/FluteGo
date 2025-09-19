package alc

import (
	"Flute_go/pkg/lct"
	"Flute_go/pkg/object"
	"Flute_go/pkg/oti"
	"encoding/binary"
	"fmt"
)

type AlcRS28 struct{}

func (c *AlcRS28) AddFti(data *[]byte, oti oti.Oti, transferLength uint64) {
	/*0                   1                   2                   3
	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	|   HET = 64    |    HEL = 3    |                               |
	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+                               +
	|                      Transfer Length (L)                      |
	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	|   Encoding Symbol Length (E)  | MaxBlkLen (B) |     max_n     |
	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+*/

	// 高 16bit = HET(8) | HEL(8)；低 48bit = transfer_length
	const het = uint64(lct.ExtFti) // 64
	const hel = uint64(3)          // 3 * 4 = 12 字节
	extHeaderL := (het << 56) | (hel << 48) | (transferLength & 0xFFFFFFFFFFFF)

	// max_n = 最大源块长 + 纠删码冗余符号数（总符号数，取低8位）
	maxN := (oti.MaxNumberOfParitySymbols + oti.MaximumSourceBlockLength) & 0xFF

	// E/B/N 打包成 32-bit：E(16) | B(8) | N(8)
	eBn := (uint32(oti.EncodingSymbolLength) << 16) |
		((oti.MaximumSourceBlockLength & 0xFF) << 8) |
		(uint32(maxN) & 0xFF)

	// 追加到 data（大端）
	var tmp8 [8]byte
	binary.BigEndian.PutUint64(tmp8[:], extHeaderL)
	*data = append(*data, tmp8[:]...)

	var tmp4 [4]byte
	binary.BigEndian.PutUint32(tmp4[:], eBn)
	*data = append(*data, tmp4[:]...)

	// 增加 LCT 头长度（HEL=3 => +3）
	lct.IncHdrLen(*data, 3)
}

// GetFti 解析 FTI 扩展，返回 (Oti, transfer_length)
func (c *AlcRS28) GetFti(pktBytes []byte, lctHeader lct.LCTHeader) (oti.Oti, uint64, error) {
	fti, err := lct.GetExt(pktBytes, &lctHeader, uint8(lct.ExtFti))
	if err != nil {
		return oti.Oti{}, 0, err
	}
	if fti == nil {
		// 等价于 Rust: Ok(None) —— 这里按你接口返回零值 + nil 表示“没有”
		return oti.Oti{}, 0, nil
	}
	if len(fti) != 12 {
		return oti.Oti{}, 0, fmt.Errorf("wrong extension size: %d", len(fti))
	}
	// 校验 HET/HEL
	if fti[0] != uint8(lct.ExtFti) {
		return oti.Oti{}, 0, fmt.Errorf("wrong HET: %d", fti[0])
	}
	if fti[1] != 3 {
		return oti.Oti{}, 0, fmt.Errorf("wrong HEL: %d", fti[1])
	}

	// 前 8 字节：HET|HEL|TransferLength(48-bit)
	x := binary.BigEndian.Uint64(fti[0:8])
	transferLength := x & 0xFFFFFFFFFFFF

	encodingSymbolLength := binary.BigEndian.Uint16(fti[8:10])
	maxBlkLen := uint32(fti[10])
	numEncodingSymbols := uint32(fti[11])

	o := oti.Oti{
		FecEncodingID:                 oti.ReedSolomonGF28,
		FecInstanceID:                 0,
		MaximumSourceBlockLength:      maxBlkLen,
		EncodingSymbolLength:          encodingSymbolLength,
		MaxNumberOfParitySymbols:      numEncodingSymbols - maxBlkLen,
		ReedSolomonGF2MSchemeSpecific: nil,  // 视你的结构定义决定
		InBandFti:                     true, // 带内
	}
	return o, transferLength, nil
}

func (c *AlcRS28) AddFecPayloadId(data *[]byte, _ oti.Oti, pkt object.Pkt) {
	sbn := pkt.Sbn & 0xFFFFFF
	esi := pkt.Esi & 0xFF
	header := (sbn << 8) | (esi & 0xFF)

	var b [4]byte
	binary.BigEndian.PutUint32(b[:], header)
	*data = append(*data, b[:]...)
}

// GetFecPayloadId 直接复用内联解析
func (c *AlcRS28) GetFecPayloadId(pkt AlcPkt, _ oti.Oti) (PayloadID, error) {
	return c.GetFecInlinePayloadId(pkt)
}

// GetFecInlinePayloadId 从 ALC 头和载荷之间的 4 字节读取 SBN/ESI
func (c *AlcRS28) GetFecInlinePayloadId(pkt AlcPkt) (PayloadID, error) {
	data := pkt.Data[pkt.DataAlcHeaderOffset:pkt.DataPayloadOffset]
	if len(data) != 4 {
		return PayloadID{}, fmt.Errorf("invalid inline payload id length: %d", len(data))
	}

	x := binary.BigEndian.Uint32(data)
	sbn := x >> 8
	esi := x & 0xFF

	return PayloadID{
		Sbn:               sbn,
		Esi:               esi,
		SourceBlockLength: nil,
	}, nil
}

// FecPayloadIdBlockLength 固定 4 字节
func (c *AlcRS28) FecPayloadIdBlockLength() uint { return 4 }

// 注册到工厂：等价于 Rust 的静态绑定
func init() {
	Register(oti.ReedSolomonGF28, &AlcRS28{})
}
