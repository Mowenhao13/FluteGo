package lct

import (
	t "Flute_go/pkg/type"
	"encoding/binary"
	"errors"
	"fmt"
)

type Cenc uint8

const (
	CencNull Cenc = iota
	CencZlib
	CencDeflate
	CencGzip
)

type Ext uint8

const (
	ExtFdt  Ext = 192
	ExtFti  Ext = 64
	ExtCenc Ext = 193
	ExtTime     = 2
)

var TOI_FDT = t.Uint128{}

type LCTHeader struct {
	Len             uint64    // 头部长度(字节)
	Cci             t.Uint128 // 拥塞控制信息
	Tsi             uint64    // 传输会话标识符
	Toi             t.Uint128 // 传输对象标识符
	Cp              uint8     // 编解码器标识符
	CloseObject     bool      // 是否关闭对象
	CloseSession    bool      // 是否关闭对话
	HeaderExtOffset uint32    // 拓展头偏移量
	Length          uint      // 数据包总长度
}

func (e Ext) String() string {
	switch e {
	case ExtFdt:
		return "FDT"
	case ExtFti:
		return "FTI"
	case ExtCenc:
		return "Cenc"
	case ExtTime:
		return "Time"
	default:
		return "Unknown"
	}
}

func (c Cenc) String() string {
	switch c {
	case CencNull:
		return "Null"
	case CencZlib:
		return "Zlib"
	case CencDeflate:
		return "Deflate"
	case CencGzip:
		return "Gzip"
	default:
		return "Unknown"
	}
}

// nbBytes128 计算 u128 的最小字节数
func nbBytes128(cci t.Uint128, min uint32) uint32 {
	// 高 64 位和低 64 位分别判断
	if cci.High&0xFFFF000000000000 != 0 {
		return 16
	}
	if cci.High&0xFFFF00000000 != 0 {
		return 14
	}
	if cci.High&0xFFFF != 0 {
		return 12
	}
	if cci.Low&0xFFFF000000000000 != 0 {
		return 10
	}
	if cci.Low&0xFFFF00000000 != 0 {
		return 8
	}
	if cci.Low&0xFFFF0000 != 0 {
		return 6
	}
	if cci.Low&0xFFFF != 0 {
		return 4
	}

	return min
}

func nbBytes64(n uint64, min uint32) uint32 {
	if (n & 0xFFFF000000000000) != 0 {
		return 8
	}

	if (n & 0xFFFF00000000) != 0 {
		return 6
	}

	if (n & 0xFFFF0000) != 0 {
		return 4
	}

	if (n & 0xFFFF) != 0 {
		return 2
	}

	return min
}

// LCT 头构建
func PushLCTHeader(
	data *[]byte,
	psi uint8,
	cci t.Uint128,
	tsi uint64,
	toi t.Uint128,
	codepoint uint8,
	closeObject bool,
	closeSession bool,
) {
	// 计算各字段长度
	cciSize := nbBytes128(cci, 0)
	tsiSize := nbBytes64(tsi, 2)
	toiSize := nbBytes128(toi, 2)

	// 构建标志位
	hTsi := (tsiSize & 2) >> 1 // Is TSI half-word ?
	hToi := (toiSize & 2) >> 1 // Is TOI half-word ?

	h := hTsi | hToi // Half-word flag
	var b, a uint8
	if closeObject {
		b = 1
	}
	if closeSession {
		a = 1
	}
	o := (toiSize >> 2) & 0x3
	s := (tsiSize >> 2) & 1
	var c uint32
	switch {
	case cciSize <= 4:
		c = 0
	case cciSize <= 8:
		c = 1
	case cciSize <= 12:
		c = 2
	default:
		c = 3
	}

	// 计算头部总长度
	hdrLen := uint8(2 + o + s + h + c)
	v := uint32(1)

	// 构建头部第一个32位字
	lctHeader := uint32(codepoint) |
		(uint32(hdrLen) << 8) |
		(uint32(b) << 16) |
		(uint32(a) << 17) |
		(uint32(h) << 20) |
		(uint32(o) << 21) |
		(uint32(s) << 23) |
		(uint32(psi) << 24) |
		(uint32(c) << 26) |
		(v << 28)

	// 写入头部 (大端序)
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], lctHeader)
	*data = append(*data, buf[:]...)

	// 写入各字段 (CCI, TSI, TOI)

	// Insert CCI
	cciNet := cci.ToBytesBE()
	cciNetStart := len(cciNet) - int((c+1)<<2)
	*data = append(*data, cciNet[cciNetStart:]...)

	// Insert TSI
	var tsiBuf [8]byte
	binary.BigEndian.PutUint64(tsiBuf[:], tsi)
	tsiNetStart := len(tsiBuf) - int((s<<2)+(h<<1))
	*data = append(*data, tsiBuf[tsiNetStart:]...)

	// Insert TOI
	toiNet := toi.ToBytesBE()
	toiNetStart := len(toiNet) - int((o<<2)+(h<<1))
	*data = append(*data, toiNet[toiNetStart:]...)
}

func IncHdrLen(data []byte, val uint8) {
	data[2] += val
}

func ParseLCTHeader(data []byte) (*LCTHeader, error) {
	if len(data) < 4 {
		return nil, errors.New("fail to read lct header size")
	}

	// 头部长度 (单位 4 字节)
	lenHdr := int(data[2]) << 2
	if lenHdr > len(data) {
		return nil, fmt.Errorf("lct header size is %d whereas pkt size is %d", lenHdr, len(data))
	}

	// 提取标志位
	cp := data[3]
	flags1 := data[0]
	flags2 := data[1]

	s := (flags2 >> 7) & 0x1
	o := (flags2 >> 5) & 0x3
	h := (flags2 >> 4) & 0x1
	c := (flags1 >> 2) & 0x3
	a := (flags2 >> 1) & 0x1
	b := flags2 & 0x1
	version := flags1 >> 4

	// 检查版本号
	if version != 1 && version != 2 {
		return nil, fmt.Errorf("FLUTE version %d is not supported", version)
	}

	// 各字段长度 (字节)
	cciLen := ((uint32(c) + 1) << 2)
	tsiLen := ((uint32(s) << 2) + (uint32(h) << 1))
	toiLen := ((uint32(o) << 2) + (uint32(h) << 1))

	cciFrom := 4
	cciTo := cciFrom + int(cciLen)
	tsiTo := cciTo + int(tsiLen)
	toiTo := tsiTo + int(toiLen)
	headerExtOffset := uint32(toiTo)

	if toiTo > len(data) || cciLen > 16 || tsiLen > 8 || toiLen > 16 {
		return nil, fmt.Errorf("toi ends to offset %d whereas pkt size is %d", toiTo, len(data))
	}

	if headerExtOffset > uint32(lenHdr) {
		return nil, errors.New("EXT offset outside LCT header")
	}

	// 提取字段 (大端序对齐到固定长度)
	var cciBuf [16]byte
	var tsiBuf [8]byte
	var toiBuf [16]byte

	copy(cciBuf[16-int(cciLen):], data[cciFrom:cciTo])
	copy(tsiBuf[8-int(tsiLen):], data[cciTo:tsiTo])
	copy(toiBuf[16-int(toiLen):], data[tsiTo:toiTo])

	cci := t.FromBytesBE(cciBuf[:])
	tsi := binary.BigEndian.Uint64(tsiBuf[:])
	toi := t.FromBytesBE(toiBuf[:])

	return &LCTHeader{
		Len:             uint64(lenHdr),
		Cci:             cci,
		Tsi:             tsi,
		Toi:             toi,
		Cp:              cp,
		CloseObject:     b != 0,
		CloseSession:    a != 0,
		HeaderExtOffset: headerExtOffset,
		Length:          uint(lenHdr),
	}, nil
}

// 拓展头处理
func GetExt(data []byte, lct *LCTHeader, ext uint8) ([]byte, error) {
	if uint64(lct.HeaderExtOffset) >= lct.Len {
		return nil, fmt.Errorf("invalid header_ext_offset=%d len=%d",
			lct.HeaderExtOffset, lct.Len)
	}

	lctExt := data[lct.HeaderExtOffset:lct.Len]

	for len(lctExt) >= 4 {
		het := lctExt[0]

		var hel int
		if het >= 128 {
			hel = 4
		} else {
			hel = int(lctExt[1]) << 2
		}

		if hel == 0 || hel > len(lctExt) {
			return nil, fmt.Errorf(
				"fail, LCT EXT size is %d/%d het=%d offset=%d",
				hel, len(lctExt), het, lct.HeaderExtOffset,
			)
		}

		if het == ext {
			// 找到目标扩展头，返回切片
			return lctExt[:hel], nil
		}

		// 跳过当前扩展头，继续解析下一个
		lctExt = lctExt[hel:]
	}

	// 没找到，返回 nil
	return nil, nil
}
