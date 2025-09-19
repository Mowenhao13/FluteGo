package alc

import (
	"Flute_go/pkg/lct"
	"Flute_go/pkg/object"
	"Flute_go/pkg/oti"
	"encoding/binary"
	"fmt"
)

type AlcRS2m struct{}

// AddFti 写入 FTI 扩展 (HET=64, HEL=4, 长度16字节)
func (c *AlcRS2m) AddFti(data *[]byte, o oti.Oti, transferLength uint64) {
	// 如果 SchemeSpecific 是 ReedSolomon
	if o.ReedSolomonGF2MSchemeSpecific != nil {
		rs := o.ReedSolomonGF2MSchemeSpecific

		extHeaderL := (uint64(lct.ExtFti) << 56) | (4 << 48) | (transferLength & 0xFFFFFFFFFFFF)

		b := uint16(o.MaximumSourceBlockLength)
		maxN := uint16(o.MaxNumberOfParitySymbols + o.MaximumSourceBlockLength)

		var buf8 [8]byte
		binary.BigEndian.PutUint64(buf8[:], extHeaderL)
		*data = append(*data, buf8[:]...)

		*data = append(*data, rs.M)
		*data = append(*data, rs.G)

		var buf2 [2]byte
		binary.BigEndian.PutUint16(buf2[:], o.EncodingSymbolLength)
		*data = append(*data, buf2[:]...)

		binary.BigEndian.PutUint16(buf2[:], b)
		*data = append(*data, buf2[:]...)

		binary.BigEndian.PutUint16(buf2[:], maxN)
		*data = append(*data, buf2[:]...)

		lct.IncHdrLen(*data, 4)
	}
}

// GetFti 解析 FTI，返回 Oti 和 transfer_length
func (c *AlcRS2m) GetFti(pktBytes []byte, lctHeader lct.LCTHeader) (oti.Oti, uint64, error) {
	fti, err := lct.GetExt(pktBytes, &lctHeader, uint8(lct.ExtFti))
	if err != nil {
		return oti.Oti{}, 0, err
	}
	if fti == nil {
		return oti.Oti{}, 0, nil // 对应 Rust Ok(None)
	}
	if len(fti) != 16 {
		return oti.Oti{}, 0, fmt.Errorf("wrong extension size: %d", len(fti))
	}
	if fti[0] != uint8(lct.ExtFti) {
		return oti.Oti{}, 0, fmt.Errorf("wrong HET: %d", fti[0])
	}
	if fti[1] != 4 {
		return oti.Oti{}, 0, fmt.Errorf("wrong HEL: %d", fti[1])
	}

	x := binary.BigEndian.Uint64(fti[0:8])
	transferLength := x & 0xFFFFFFFFFFFF

	m := fti[8]
	g := fti[9]
	encodingSymbolLength := binary.BigEndian.Uint16(fti[10:12])
	b := binary.BigEndian.Uint16(fti[12:14])
	maxN := binary.BigEndian.Uint16(fti[14:16])

	o := oti.Oti{
		FecEncodingID:            oti.ReedSolomonGF2M,
		FecInstanceID:            0,
		MaximumSourceBlockLength: uint32(b),
		EncodingSymbolLength:     encodingSymbolLength,
		MaxNumberOfParitySymbols: uint32(maxN) - uint32(b),
		ReedSolomonGF2MSchemeSpecific: &oti.ReedSolomonGF2MSchemeSpecific{
			G: func() uint8 {
				if g == 0 {
					return 1
				}
				return g
			}(),
			M: func() uint8 {
				if m == 0 {
					return 8
				}
				return m
			}(),
		},
		InBandFti: true,
	}
	return o, transferLength, nil
}

// AddFecPayloadId 写入 (SBN << m) | ESI
func (c *AlcRS2m) AddFecPayloadId(data *[]byte, o oti.Oti, pkt object.Pkt) {
	m := uint8(8)
	if o.ReedSolomonGF2MSchemeSpecific != nil {
		rs := o.ReedSolomonGF2MSchemeSpecific
		m = rs.M
	}
	sbn := pkt.Sbn
	esi := pkt.Esi

	header := (sbn << m) | (esi & 0xFF)

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], header)
	*data = append(*data, buf[:]...)
}

// GetFecPayloadId 从 pkt 中解析 (SBN, ESI)
func (c *AlcRS2m) GetFecPayloadId(pkt AlcPkt, o oti.Oti) (PayloadID, error) {
	data := pkt.Data[pkt.DataAlcHeaderOffset:pkt.DataPayloadOffset]
	if len(data) != 4 {
		return PayloadID{}, fmt.Errorf("invalid payload id length: %d", len(data))
	}
	x := binary.BigEndian.Uint32(data)

	m := uint8(8)
	if o.ReedSolomonGF2MSchemeSpecific != nil {
		rs := o.ReedSolomonGF2MSchemeSpecific
		m = rs.M
	}

	sbn := x >> m
	esiMask := (uint32(1) << m) - 1
	esi := x & esiMask

	return PayloadID{
		Sbn:               sbn,
		Esi:               esi,
		SourceBlockLength: nil,
	}, nil
}

// GetFecInlinePayloadId 暂不支持
func (c *AlcRS2m) GetFecInlinePayloadId(_ AlcPkt) (PayloadID, error) {
	return PayloadID{}, fmt.Errorf("not supported")
}

// FecPayloadIdBlockLength 固定4字节
func (c *AlcRS2m) FecPayloadIdBlockLength() uint { return 4 }

// 注册到工厂
func init() {
	Register(oti.ReedSolomonGF2M, &AlcRS2m{})
}
