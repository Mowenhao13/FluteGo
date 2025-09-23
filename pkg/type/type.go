package _type

import (
	"encoding/binary"
	"fmt"
	"math/bits"
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

// Equal 判断是否相等
func (u Uint128) Equal(v Uint128) bool {
	return u.High == v.High && u.Low == v.Low
}

// Less 判断 u < v
func (u Uint128) Less(v Uint128) bool {
	if u.High < v.High {
		return true
	}
	if u.High > v.High {
		return false
	}
	return u.Low < v.Low
}

// Greater 判断 u > v
func (u Uint128) Greater(v Uint128) bool {
	if u.High > v.High {
		return true
	}
	if u.High < v.High {
		return false
	}
	return u.Low > v.Low
}

// Add 计算 u + v，返回结果和是否溢出
func (u Uint128) Add(v Uint128) (res Uint128, carry bool) {
	lo, c := bits.Add64(u.Low, v.Low, 0)
	hi, c2 := bits.Add64(u.High, v.High, c)
	return Uint128{High: hi, Low: lo}, c2 != 0
}

// Sub 计算 u - v，返回结果和是否借位
func (u Uint128) Sub(v Uint128) (res Uint128, borrow bool) {
	lo, b := bits.Sub64(u.Low, v.Low, 0)
	hi, b2 := bits.Sub64(u.High, v.High, b)
	return Uint128{High: hi, Low: lo}, b2 != 0
}

// ToUint64 截断到 64bit
func (u Uint128) ToUint64() uint64 {
	return u.Low
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
func (u Uint128) String() string { return fmt.Sprintf("%016x%016x", u.High, u.Low) } // 16+16位hex

func StringToUint128(s string) Uint128 {
	// 16+16位hex → 128bit
	if len(s) != 32 {
		fmt.Errorf("invalid Uint128 string length: %d", len(s))
		return Uint128{}
	}
	var u Uint128
	// 前 16 hex = High
	highStr := s[:16]
	lowStr := s[16:]

	var high, low uint64
	_, err := fmt.Sscanf(highStr, "%016x", &high)
	if err != nil {
		return u
	}
	_, err = fmt.Sscanf(lowStr, "%016x", &low)
	if err != nil {
		return u
	}

	u.High = high
	u.Low = low
	return u
}
