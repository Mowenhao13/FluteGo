package fec

import (
	"fmt"
	"log"

	rs "github.com/klauspost/reedsolomon"
)

type RSCodecParam struct {
	NbSourceSymbols      uint
	NbParitySymbols      uint
	EncodingSymbolLength uint
}

type RSGalois8Codec struct {
	Params                    RSCodecParam
	Rs                        rs.Encoder
	DecodeShards              [][]byte
	DecodeBlock               []byte
	NbSourceSymbolsReceived   uint
	NbEncodingSymbolsReceived uint
}

func (param *RSCodecParam) createShards(data []byte) ([][]byte, error) {
	log.Printf("Creating shards with nb_source_symbols=%d, nb_parity_symbols=%d, encoding_symbol_length=%d",
		param.NbSourceSymbols, param.NbParitySymbols, param.EncodingSymbolLength)

	var shards [][]byte
	for i := uint(0); i < uint(len(data)); i += param.EncodingSymbolLength {
		end := i + param.EncodingSymbolLength
		if end > uint(len(data)) {
			end = uint(len(data))
		}
		chunk := make([]byte, end-i)
		copy(chunk, data[i:end])
		shards = append(shards, chunk)
	}

	// 填充最后一个分片
	last := shards[len(shards)-1]
	if uint(len(last)) < param.EncodingSymbolLength {
		log.Printf("Padding last shard from %d to %d bytes", len(last), param.EncodingSymbolLength)
		padded := make([]byte, param.EncodingSymbolLength)
		copy(padded, last)
		shards[len(shards)-1] = padded
	}

	// 检查源符号数
	if uint(len(shards)) != param.NbSourceSymbols {
		return nil, fmt.Errorf("nb source symbols is %d instead of %d", len(shards), param.NbSourceSymbols)
	}

	// 追加冗余分片
	for i := uint(0); i < param.NbParitySymbols; i++ {
		shards = append(shards, make([]byte, param.EncodingSymbolLength))
	}

	return shards, nil
}

// NewRSGalois8Codec 创建新的 RS 编码器
func NewRSGalois8Codec(nbSourceSymbols, nbParitySymbols, encodingSymbolLength uint) (*RSGalois8Codec, error) {
	log.Printf(
		"Creating new RS codec with: source_symbols=%d, parity_symbols=%d, symbol_length=%d",
		nbSourceSymbols, nbParitySymbols, encodingSymbolLength,
	)

	// 创建 reedsolomon 编码器
	enc, err := rs.New(int(nbSourceSymbols), int(nbParitySymbols))
	if err != nil {
		return nil, fmt.Errorf("fail to create RS codec: %w", err)
	}

	codec := &RSGalois8Codec{
		Params: RSCodecParam{
			NbSourceSymbols:      nbSourceSymbols,
			NbParitySymbols:      nbParitySymbols,
			EncodingSymbolLength: encodingSymbolLength,
		},
		Rs:                        enc,
		DecodeShards:              make([][]byte, nbSourceSymbols+nbParitySymbols),
		DecodeBlock:               nil,
		NbSourceSymbolsReceived:   0,
		NbEncodingSymbolsReceived: 0,
	}

	return codec, nil
}

func (codec *RSGalois8Codec) PushSymbol(encodingSymbol []byte, esi uint32) {
	if codec.DecodeBlock != nil {
		log.Printf("Block already decoded, ignoring new symbol")
		return
	}
	log.Printf("Receive ESI %d (length: %d bytes)", esi, len(encodingSymbol))
	if int(esi) >= len(codec.DecodeShards) {
		log.Printf("ESI %d out of range (max %d)", esi, len(codec.DecodeShards))
		return
	}

	if codec.DecodeShards[esi] != nil {
		log.Printf("Already received symbol for ESI %d", esi)
		return
	}

	// 存储 shard
	shardCopy := make([]byte, len(encodingSymbol))
	copy(shardCopy, encodingSymbol)
	codec.DecodeShards[esi] = shardCopy

	if esi < uint32(codec.Params.NbSourceSymbols) {
		codec.NbSourceSymbolsReceived++
	}
	codec.NbEncodingSymbolsReceived++

	log.Printf(
		"Received symbols: %d/%d source, %d/%d total",
		codec.NbSourceSymbolsReceived, codec.Params.NbSourceSymbols,
		codec.NbEncodingSymbolsReceived, codec.Params.NbSourceSymbols+codec.Params.NbParitySymbols,
	)
}

func (codec *RSGalois8Codec) CanDecode() bool {
	canDecode := codec.NbEncodingSymbolsReceived >= uint(codec.Params.NbSourceSymbols)
	log.Printf(
		"Can decode: %t (have %d symbols, need %d)",
		canDecode,
		codec.NbEncodingSymbolsReceived,
		codec.Params.NbSourceSymbols,
	)
	return canDecode
}

// Decode 尝试进行解码
func (codec *RSGalois8Codec) Decode() bool {
	if codec.DecodeBlock != nil {
		log.Printf("Block already decoded")
		return true
	}

	if codec.NbSourceSymbolsReceived < uint(codec.Params.NbSourceSymbols) {
		log.Printf(
			"Attempting reconstruction (have %d/%d source symbols)",
			codec.NbSourceSymbolsReceived, codec.Params.NbSourceSymbols,
		)
		err := codec.Rs.Reconstruct(codec.DecodeShards)
		if err != nil {
			log.Printf("Reconstruction failed: %v", err)
			return false
		}
		log.Printf("Reconstruct with success !")
	}

	// 拼接 source block
	var output []byte
	for i := uint(0); i < codec.Params.NbSourceSymbols; i++ {
		if codec.DecodeShards[i] == nil {
			log.Printf("Missing shard at index %d", i)
			return false
		}
		output = append(output, codec.DecodeShards[i]...)
	}

	log.Printf("Successfully decoded block (%d bytes)", len(output))
	codec.DecodeBlock = output
	return true
}

func (codec *RSGalois8Codec) SourceBlock() ([]byte, error) {
	if codec.DecodeBlock == nil {
		log.Printf("Block not decoded yet")
		return nil, fmt.Errorf("block not decoded")
	}
	return codec.DecodeBlock, nil
}

func (codec *RSGalois8Codec) Encode(data []byte) ([]FecShard, error) {
	log.Printf("Encoding data (%d bytes)", len(data))

	shards, err := codec.Params.createShards(data)
	if err != nil {
		return nil, fmt.Errorf("fail to create shards: %w", err)
	}

	log.Printf("Encoding shards with Reed-Solomon")
	if err := codec.Rs.Encode(shards); err != nil {
		return nil, fmt.Errorf("fail to encode shards: %w", err)
	}

	result := make([]FecShard, 0, len(shards))
	for i, shard := range shards {
		result = append(result, &DataFecShard{
			Shard: shard,
			Index: uint32(i),
		})
	}

	log.Printf("Successfully encode %d shards with Reed-Solomon", len(result))
	return result, nil
}
