package _type

import "encoding/binary"

type Uint128 struct {
	High uint64
	Low  uint64
}

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
