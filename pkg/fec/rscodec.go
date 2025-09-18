package fec

import (
	//"Flute_go/internal/common"
	rs "github.com/klauspost/reedsolomon"
)

type RsCodecParam struct {
	NbSourceSymbols      uint
	NbParitySymbols      uint
	EncodingSymbolLength uint
}

type RSGalois8Codec struct {
	Params                    RsCodecParam
	Rs                        rs.Encoder
	DecodeShards              [][]byte
	DecodeBlock               []byte
	NbSourceSymbolsReceived   uint
	NbEncodingSymbolsReceived uint
}

func (param *RsCodecParam) createShards(data []byte) ([][]byte, error) {
	//TODO
}

func NewRSGalois8Codec(nbSourceSymbols, nbParitySymbols, encodingSymbolLength uint) *RSGalois8Codec {
	//TODO
	return &RSGalois8Codec{}
}

func (codec *RSGalois8Codec) PushSymbol(encodingSymbol []byte, esi uint32) ([]byte, error) {
	//TODO
}

func (codec *RSGalois8Codec) CanDecode() bool {
	//TODO
}

func (codec *RSGalois8Codec) Decode() bool {
	//TODO
}

func (codec *RSGalois8Codec) SourceBlock() ([]byte, error) {
	//TODO
}

func (codec *RSGalois8Codec) Encode(data []byte) ([]FecShard, error) {
	//TODO
}
