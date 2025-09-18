package oti

import (
	"errors"
	"fmt"
)

type FECEncodingID uint8

const (
	NoCode FECEncodingID = iota
	ReedSolomonGF2M
	ReedSolomonGF28
	ReedSolomonGF28UnderSpecified
)

func (f FECEncodingID) String() string {
	switch f {
	case NoCode:
		return "NoCode"
	case ReedSolomonGF2M:
		return "ReedSolomonGF2M"
	case ReedSolomonGF28:
		return "ReedSolomonGF28"
	case ReedSolomonGF28UnderSpecified:
		return "ReedSolomonGF28UnderSpecified"
	default:
		return fmt.Sprintf("Unknown FECEncodingID (%d)", f)
	}
}

func FECEncodingIDFromByte(v byte) (FECEncodingID, error) {
	switch v {
	case 0:
		return NoCode, nil
	case 1:
		return ReedSolomonGF2M, nil
	case 2:
		return ReedSolomonGF28, nil
	case 3:
		return ReedSolomonGF28UnderSpecified, nil
	default:
		return 0, errors.New("invalid FECEncodingID")
	}
}

type ReedSolomonGF2MSchemeSpecific struct {
	/// Length of the finite field elements, in bits
	M uint8
	/// number of encoding symbols per group used for the object
	/// The default value is 1, meaning that each packet contains exactly one symbol
	G uint8
}

func (r ReedSolomonGF2MSchemeSpecific) SchemeSpecific() string {

}

func (r ReedSolomonGF2MSchemeSpecific) Decode(fec_oti_scheme_specific_info string) (ReedSolomonGF2MSchemeSpecific, error) {

}

type Oti struct {
	FecEncodingID                 FECEncodingID
	FecInstanceID                 uint16
	MaximumSourceBlockLength      uint32
	EncodingSymbolLength          uint16
	MaxNumberOfParitySymbols      uint32
	ReedSolomonGF2MSchemeSpecific *ReedSolomonGF2MSchemeSpecific
	InhandFti                     bool
}

type OtiAttributes struct {
	FecOtiFecEncodingID              *uint8
	FecOtiFecInstanceID              *uint64
	FecOtiMaximumSourceBlockLength   *uint64
	FecOtiEncodingSymbolLength       *uint64
	FecOtiMaxNumberOfEncodingSymbols *uint64
	FecOtiSchemeSpecificInfo         *string
}

func NewOti() *Oti {
	return &Oti{
		FecEncodingID:                 NoCode,
		FecInstanceID:                 0,
		MaximumSourceBlockLength:      64,
		EncodingSymbolLength:          1424,
		ReedSolomonGF2MSchemeSpecific: nil,
		InhandFti:                     true,
	}
}

func NewNoCode(encodingSymbolLength uint16, maximumSourceBlockLength uint32) *Oti {
	return &Oti{
		FecEncodingID:                 NoCode,
		FecInstanceID:                 0,
		MaximumSourceBlockLength:      maximumSourceBlockLength,
		EncodingSymbolLength:          encodingSymbolLength,
		ReedSolomonGF2MSchemeSpecific: nil,
		InhandFti:                     true,
	}
}

func NewReedSolomonRS28(encodingSymbolLength uint16, maximumSourceBlockLength uint32, maxNumberOfParitySymbols uint8) (*Oti, error) {
	return &Oti{
		FecEncodingID:                 ReedSolomonGF28,
		FecInstanceID:                 0,
		MaximumSourceBlockLength:      maximumSourceBlockLength,
		EncodingSymbolLength:          encodingSymbolLength,
		MaxNumberOfParitySymbols:      uint32(maxNumberOfParitySymbols),
		ReedSolomonGF2MSchemeSpecific: nil,
		InhandFti:                     true,
	}, nil
}

func NewReedSolomonRs28UnderSpecified(encodingSymbolLength uint16, maximumSourceBlockLength uint32, maxNumberOfParitySymbols uint16) (*Oti, error) {
	return &Oti{
		FecEncodingID:                 ReedSolomonGF28UnderSpecified,
		FecInstanceID:                 0,
		MaximumSourceBlockLength:      maximumSourceBlockLength,
		EncodingSymbolLength:          encodingSymbolLength,
		MaxNumberOfParitySymbols:      uint32(maxNumberOfParitySymbols),
		ReedSolomonGF2MSchemeSpecific: nil,
		InhandFti:                     true,
	}, nil
}

func (o *Oti) MaxTransferLength() uint64 {
	var transferlength uint64
	switch o.FecEncodingID {
	case NoCode, ReedSolomonGF2M, ReedSolomonGF28:
		transferlength = 0xFFFFFFFFFFFF // 48bits
	default:
		transferlength = 0
	}
	return transferlength
}

func (o *Oti) MaxSourceBlockNumber() uint64 {
	var maxU32 uint32 = ^uint32(0)
	var maxU16 uint16 = ^uint16(0)
	var maxU8 uint8 = ^uint8(0)
	switch o.FecEncodingID {
	case NoCode:
		return uint64(maxU16)
	case ReedSolomonGF2M:
		//TODO
		return 0
	case ReedSolomonGF28:
		return uint64(maxU8)
	case ReedSolomonGF28UnderSpecified:
		return uint64(maxU32)
	}
}

func (o *Oti) GetAttributes()
