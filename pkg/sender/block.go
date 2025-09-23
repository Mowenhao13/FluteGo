package sender

import (
	"Flute_go/pkg/fec"
	"Flute_go/pkg/oti"
	"Flute_go/pkg/tools"
	"errors"
	"log"
)

type Block struct {
	sbn             uint32
	readIndex       uint32
	shards          []fec.FecShard
	NbSourceSymbols uint
}

type EncodingSymbol struct {
	Sbn            uint32
	Esi            uint32
	Symbols        []byte
	IsSourceSymbol bool
}

func NewBlockFromBuffer(
	sbn uint32,
	buffer []byte,
	blockLength uint64,
	o *oti.Oti,
) (*Block, error) {
	nbSourceSymbols := uint(tools.DivCeil(uint64(len(buffer)), uint64(o.EncodingSymbolLength)))

	log.Printf("buffer_len=%d nb_source_symbols=%d encoding_symbol_length=%d",
		len(buffer), nbSourceSymbols, o.EncodingSymbolLength)

	var shards []fec.FecShard
	var err error

	switch o.FecEncodingID {
	case oti.NoCode:
		shards = createShardsNoCode(o, buffer)

	case oti.ReedSolomonGF28, oti.ReedSolomonGF28UnderSpecified, oti.ReedSolomonGF2M:
		shards, err = createShardsReedSolomonGF8(o, int(nbSourceSymbols), int(blockLength), buffer)
		if err != nil {
			return nil, err
		}

	default:
		return nil, errors.New("unknown FEC encoding ID")
	}

	return &Block{
		sbn:             sbn,
		readIndex:       0,
		shards:          shards,
		NbSourceSymbols: nbSourceSymbols,
	}, nil
}

// IsEmpty 判断 block 是否读完
func (b *Block) IsEmpty() bool {
	return int(b.readIndex) == len(b.shards)
}

// Read 读取一个编码符号（返回符号和是否已读完）
func (b *Block) Read() (*EncodingSymbol, bool) {
	if b.IsEmpty() {
		return nil, true
	}
	shard := b.shards[b.readIndex]
	esi := shard.ESI()
	isSourceSymbol := uint(esi) < b.NbSourceSymbols

	symbol := &EncodingSymbol{
		Sbn:            b.sbn,
		Esi:            esi,
		Symbols:        shard.Data(),
		IsSourceSymbol: isSourceSymbol,
	}
	b.readIndex++
	return symbol, b.IsEmpty()
}

// ------------------- 分片生成函数 -------------------

// NoCode 分片：直接按符号长度切分
func createShardsNoCode(o *oti.Oti, buffer []byte) []fec.FecShard {
	shards := make([]fec.FecShard, 0)
	chunkLen := int(o.EncodingSymbolLength)

	for i := 0; i < len(buffer); i += chunkLen {
		end := i + chunkLen
		if end > len(buffer) {
			end = len(buffer)
		}
		chunk := buffer[i:end]
		index := uint32(i / chunkLen)
		shards = append(shards, fec.NewDataFecShard(chunk, index))
	}
	return shards
}

// Reed-Solomon GF(2^8) 分片
func createShardsReedSolomonGF8(o *oti.Oti, nbSourceSymbols, blockLength int, buffer []byte) ([]fec.FecShard, error) {
	if nbSourceSymbols > int(o.MaximumSourceBlockLength) {
		return nil, errors.New("nbSourceSymbols exceeds MaximumSourceBlockLength")
	}
	if nbSourceSymbols > blockLength {
		return nil, errors.New("nbSourceSymbols exceeds blockLength")
	}
	encoder, err := fec.NewRSGalois8Codec(uint(nbSourceSymbols), uint(o.MaxNumberOfParitySymbols), uint(o.EncodingSymbolLength))
	if err != nil {
		return nil, err
	}
	return encoder.Encode(buffer)
}
