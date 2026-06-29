/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package meta

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// LCT 头部常量定义 (RFC 5651)
const (
	LCTVersion = 1

	// TOI 特殊值
	TOIFDT = 0 // FDT 使用 TOI=0

	// LCT 头部长度 (字节)
	// V(4) + C(2) + PSI(2) + S(1) + O(2) + H(1) + Res(2) + A(1) + B(1) + HDR_LEN(8) + CP(8) = 32 bits = 4 bytes
	// CCI = 32 bits = 4 bytes
	// TSI = 32 bits = 4 bytes
	// TOI = 32 bits = 4 bytes
	// Chunk Index = 32 bits = 4 bytes
	// Symbol ID = 32 bits = 4 bytes
	// Total = 24 bytes
	LCTHeaderLength = 24
)

// LCTHeader 表示 LCT 头部 (RFC 5651)
type LCTHeader struct {
	// 第一个 32 位字
	Version    uint8  // 4 bits, 必须为 1
	C          uint8  // 2 bits, Congestion Control
	PSI        uint8  // 2 bits, Packet Sequence Identifier
	S          bool   // 1 bit, Session Identifier (TSI 存在)
	O          uint8  // 2 bits, Object Identifier (TOI 长度)
	H          bool   // 1 bit, Half-word
	Res        uint8  // 2 bits, Reserved
	A          bool   // 1 bit, Close Session
	B          bool   // 1 bit, Close Object
	HdrLen     uint8  // 8 bits, Header length in 32-bit words
	CP         uint8  // 8 bits, Codepoint (FEC Encoding ID)

	// 后续字段
	CCI        uint32 // 32 bits, Congestion Control Information
	TSI        uint32 // 32 bits, Transport Session Identifier
	TOI        uint32 // 32 bits, Transport Object Identifier
	ChunkIndex uint32 // 32 bits, Chunk Index / SBN
	SymbolID   uint32 // 32 bits, Symbol ID / ESI
}

// Encode 将 LCT 头部编码为字节流
func (h *LCTHeader) Encode() ([]byte, error) {
	buf := new(bytes.Buffer)

	// 构建第一个 32 位字
	// V(4) + C(2) + PSI(2) + S(1) + O(2) + H(1) + Res(2) + A(1) + B(1) + HDR_LEN(8) + CP(8)
	var firstWord uint32
	firstWord |= uint32(h.Version&0x0F) << 28
	firstWord |= uint32(h.C&0x03) << 26
	firstWord |= uint32(h.PSI&0x03) << 24
	if h.S {
		firstWord |= 1 << 23
	}
	firstWord |= uint32(h.O&0x03) << 21
	if h.H {
		firstWord |= 1 << 20
	}
	firstWord |= uint32(h.Res&0x03) << 18
	if h.A {
		firstWord |= 1 << 17
	}
	if h.B {
		firstWord |= 1 << 16
	}
	firstWord |= uint32(h.HdrLen&0xFF) << 8
	firstWord |= uint32(h.CP & 0xFF)

	if err := binary.Write(buf, binary.BigEndian, firstWord); err != nil {
		return nil, err
	}

	// CCI
	if err := binary.Write(buf, binary.BigEndian, h.CCI); err != nil {
		return nil, err
	}

	// TSI
	if err := binary.Write(buf, binary.BigEndian, h.TSI); err != nil {
		return nil, err
	}

	// TOI
	if err := binary.Write(buf, binary.BigEndian, h.TOI); err != nil {
		return nil, err
	}

	// Chunk Index
	if err := binary.Write(buf, binary.BigEndian, h.ChunkIndex); err != nil {
		return nil, err
	}

	// Symbol ID
	if err := binary.Write(buf, binary.BigEndian, h.SymbolID); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// Decode 从字节流解码 LCT 头部
func (h *LCTHeader) Decode(data []byte) error {
	if len(data) < LCTHeaderLength {
		return errors.New("data too short for LCT header")
	}

	buf := bytes.NewReader(data)

	// 读取第一个 32 位字
	var firstWord uint32
	if err := binary.Read(buf, binary.BigEndian, &firstWord); err != nil {
		return err
	}

	// 解析各个字段
	h.Version = uint8((firstWord >> 28) & 0x0F)
	h.C = uint8((firstWord >> 26) & 0x03)
	h.PSI = uint8((firstWord >> 24) & 0x03)
	h.S = (firstWord>>23)&0x01 == 1
	h.O = uint8((firstWord >> 21) & 0x03)
	h.H = (firstWord>>20)&0x01 == 1
	h.Res = uint8((firstWord >> 18) & 0x03)
	h.A = (firstWord>>17)&0x01 == 1
	h.B = (firstWord>>16)&0x01 == 1
	h.HdrLen = uint8((firstWord >> 8) & 0xFF)
	h.CP = uint8(firstWord & 0xFF)

	// 验证版本
	if h.Version != LCTVersion {
		return errors.New("invalid LCT version")
	}

	// CCI
	if err := binary.Read(buf, binary.BigEndian, &h.CCI); err != nil {
		return err
	}

	// TSI
	if err := binary.Read(buf, binary.BigEndian, &h.TSI); err != nil {
		return err
	}

	// TOI
	if err := binary.Read(buf, binary.BigEndian, &h.TOI); err != nil {
		return err
	}

	// Chunk Index
	if err := binary.Read(buf, binary.BigEndian, &h.ChunkIndex); err != nil {
		return err
	}

	// Symbol ID
	if err := binary.Read(buf, binary.BigEndian, &h.SymbolID); err != nil {
		return err
	}

	return nil
}

// NewLCTHeader 创建一个新的 LCT 头部
func NewLCTHeader(toi uint32, cp uint8, tsi uint32) *LCTHeader {
	return &LCTHeader{
		Version: LCTVersion,
		C:       0,
		PSI:     0,
		S:       true, // TSI 存在
		O:       2,    // TOI 32 bits
		H:       false,
		Res:     0,
		A:       false,
		B:       false,
		HdrLen:  6, // 24 bytes / 4 = 6
		CP:      cp,
		CCI:     0,
		TSI:     tsi,
		TOI:     toi,
	}
}

// IsFDT 判断是否为 FDT 包 (TOI=0)
func (h *LCTHeader) IsFDT() bool {
	return h.TOI == TOIFDT
}

// SetCloseSession 设置关闭会话标志
func (h *LCTHeader) SetCloseSession() {
	h.A = true
}

// SetCloseObject 设置关闭对象标志
func (h *LCTHeader) SetCloseObject() {
	h.B = true
}
