package _type

import (
	"encoding/binary"
	"fmt"
)

type Uint128 struct {
	High uint64
	Low  uint64
}

// 构造/转换
func FromUint64(v uint64) Uint128 { return Uint128{High: 0, Low: v} }
func FromUint8(v uint8) Uint128   { return Uint128{High: 0, Low: uint64(v)} }

func (u Uint128) ToBytesBE() []byte {
	buf := make([]byte, 16)
	binary.BigEndian.PutUint64(buf[:8], u.High)
	binary.BigEndian.PutUint64(buf[8:], u.Low)
	return buf
}

func FromBytesBE(b []byte) Uint128 {
	if len(b) != 16 {
		panic("Uint128FromBytesBE requires 16 bytes")
	}
	return Uint128{
		High: binary.BigEndian.Uint64(b[:8]),
		Low:  binary.BigEndian.Uint64(b[8:]),
	}
}

func (u Uint128) AddUint64(v uint64) Uint128 {
	low := u.Low + v
	high := u.High
	if low < u.Low { // 检测溢出
		high++
	}
	return Uint128{High: high, Low: low}
}

// 仅对低 64 位做 AND（高位清零）
func (u Uint128) And64(mask uint64) Uint128 {
	return Uint128{High: 0, Low: u.Low & mask}
}

// 通用按位与（高/低位都参与）
func (u Uint128) And(v Uint128) Uint128 {
	return Uint128{High: u.High & v.High, Low: u.Low & v.Low}
}

// 便捷比较/显示
func (u Uint128) Equal(v Uint128) bool { return u.High == v.High && u.Low == v.Low }
func (u Uint128) String() string       { return fmt.Sprintf("%016x%016x", u.High, u.Low) } // 16+16位hex
