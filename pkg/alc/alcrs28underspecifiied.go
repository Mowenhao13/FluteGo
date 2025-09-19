// pkg/alc/alcrs28_underspecified.go
package alc

import (
	"encoding/binary"
	"fmt"

	"Flute_go/pkg/lct"
	"Flute_go/pkg/object"
	"Flute_go/pkg/oti"
)

// AlcRS28UnderSpecified 对应 Rust 的 alcrs28underspecified::AlcRS28UnderSpecified
type AlcRS28UnderSpecified struct{}

// AddFti 写入 FTI 扩展：HET=Fti, HEL=4，然后是 TL(64bits里用到高48)+FEC Instance(16) + E(16) + B(16) + max_n(16)
func (c *AlcRS28UnderSpecified) AddFti(data *[]byte, o oti.Oti, transferLength uint64) {
	/*
	 * 0                   1                   2                   3
	 * 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 * |                      Transfer Length                          |
	 * +                               +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 * |                               |         FEC Instance ID       |
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 * |    Encoding Symbol Length     |  Maximum Source Block Length  |
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 * | Max. Num. of Encoding Symbols |
	 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	 */

	// ext_header: [HET(8)=Fti | HEL(8)=4]
	extHeader := (uint16(lct.ExtFti) << 8) | 4

	// 将 TL 放在高 48 位，与 16 位 FEC Instance 拼成 64 位
	transferHeaderFecID := (transferLength << 16) | uint64(o.FecInstanceID)

	esl := o.EncodingSymbolLength
	sbl := uint16(o.MaximumSourceBlockLength & 0xFFFF)
	mne := uint16((o.MaxNumberOfParitySymbols + o.MaximumSourceBlockLength) & 0xFFFF)

	var u16 [2]byte
	var u64 [8]byte

	// 写入：ext_header (2)
	binary.BigEndian.PutUint16(u16[:], extHeader)
	*data = append(*data, u16[:]...)

	// 写入：transfer_length(48)+fec_instance_id(16) (8)
	binary.BigEndian.PutUint64(u64[:], transferHeaderFecID)
	*data = append(*data, u64[:]...)

	// 写入：E(2) | B(2) | max_n(2)
	binary.BigEndian.PutUint16(u16[:], esl)
	*data = append(*data, u16[:]...)

	binary.BigEndian.PutUint16(u16[:], sbl)
	*data = append(*data, u16[:]...)

	binary.BigEndian.PutUint16(u16[:], mne)
	*data = append(*data, u16[:]...)

	// 扩展头长度增加 4（HEL）
	lct.IncHdrLen(*data, 4)
}

// GetFti 解析 FTI，返回 Oti 与 transfer_length
func (c *AlcRS28UnderSpecified) GetFti(pktBytes []byte, lctHeader lct.LCTHeader) (oti.Oti, uint64, error) {
	fti, err := lct.GetExt(pktBytes, &lctHeader, uint8(lct.ExtFti))
	if err != nil {
		return oti.Oti{}, 0, err
	}
	if fti == nil {
		// 对应 Rust: Ok(None)
		return oti.Oti{}, 0, nil
	}
	if len(fti) != 16 {
		return oti.Oti{}, 0, fmt.Errorf("wrong extension size: %d", len(fti))
	}
	// fti[0]=HET，fti[1]=HEL
	if fti[0] != uint8(lct.ExtFti) {
		return oti.Oti{}, 0, fmt.Errorf("wrong HET: %d", fti[0])
	}
	if fti[1] != 4 {
		return oti.Oti{}, 0, fmt.Errorf("wrong exten header size %d != 4 for FTI", fti[1])
	}

	// TL 在 fti[2..10] 的高 48 位，右移 16 得到 TL
	x := binary.BigEndian.Uint64(fti[2:10])
	transferLength := x >> 16

	fecInstanceID := binary.BigEndian.Uint16(fti[8:10])
	encodingSymbolLength := binary.BigEndian.Uint16(fti[10:12])
	maximumSourceBlockLength := binary.BigEndian.Uint16(fti[12:14])
	numEncodingSymbols := binary.BigEndian.Uint16(fti[14:16])

	// 计算 parity = total - sbl（防止下溢）
	var parity uint32
	if uint32(numEncodingSymbols) >= uint32(maximumSourceBlockLength) {
		parity = uint32(numEncodingSymbols) - uint32(maximumSourceBlockLength)
	} else {
		parity = 0
	}

	o := oti.Oti{
		FecEncodingID:            oti.ReedSolomonGF28UnderSpecified,
		FecInstanceID:            fecInstanceID,
		MaximumSourceBlockLength: uint32(maximumSourceBlockLength),
		EncodingSymbolLength:     encodingSymbolLength,
		MaxNumberOfParitySymbols: parity,
		// 本模式无额外 scheme-specific
		InBandFti: true,
	}
	return o, transferLength, nil
}

// AddFecPayloadId 写入 8 字节：SBN(32) | SBL(16) | ESI(16)
func (c *AlcRS28UnderSpecified) AddFecPayloadId(data *[]byte, _ oti.Oti, pkt object.Pkt) {
	sbn := pkt.Sbn
	sourceBlockLength := uint16(pkt.SourceBlockLength)
	esi := uint16(pkt.Esi)

	var b4 [4]byte
	var b2 [2]byte

	// SBN 4 字节
	binary.BigEndian.PutUint32(b4[:], sbn)
	*data = append(*data, b4[:]...)

	// SBL 2 字节
	binary.BigEndian.PutUint16(b2[:], sourceBlockLength)
	*data = append(*data, b2[:]...)

	// ESI 2 字节
	binary.BigEndian.PutUint16(b2[:], esi)
	*data = append(*data, b2[:]...)
}

// GetFecPayloadId 复用内联解析
func (c *AlcRS28UnderSpecified) GetFecPayloadId(pkt AlcPkt, _ oti.Oti) (PayloadID, error) {
	return c.GetFecInlinePayloadId(pkt)
}

// GetFecInlinePayloadId 解析 8 字节：SBN(32) | SBL(16) | ESI(16)
func (c *AlcRS28UnderSpecified) GetFecInlinePayloadId(pkt AlcPkt) (PayloadID, error) {
	data := pkt.Data[pkt.DataAlcHeaderOffset:pkt.DataPayloadOffset]
	if len(data) != 8 {
		return PayloadID{}, fmt.Errorf("invalid inline payload id length: %d", len(data))
	}

	x := binary.BigEndian.Uint64(data)
	sbn := uint32((x >> 32) & 0xFFFFFFFF)
	sbl := uint32((x >> 16) & 0xFFFF)
	esi := uint32(x & 0xFFFF)

	return PayloadID{
		Sbn: sbn,
		Esi: esi,
		// 可选字段用指针表示
		SourceBlockLength: func(v uint32) *uint32 { return &v }(sbl),
	}, nil
}

// FecPayloadIdBlockLength 固定 8 字节
func (c *AlcRS28UnderSpecified) FecPayloadIdBlockLength() uint { return 8 }

// 注册到工厂
func init() {
	Register(oti.ReedSolomonGF28UnderSpecified, &AlcRS28UnderSpecified{})
}
