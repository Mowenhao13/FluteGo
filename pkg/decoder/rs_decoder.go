package decoder

import (
	"bytes"
	"fmt"
	"log"
	"sync"

	rs "github.com/klauspost/reedsolomon"
)

type RsDecoder struct {
	Config          DecoderConfig
	output          OutputHandler
	RsChunkDecoders sync.Map
	rsEnc           rs.Encoder
}

func NewRsDecoder(config DecoderConfig, output OutputHandler) (*RsDecoder, error) {
	enc, err := rs.New(int(config.DataShards), int(config.ParityShards))
	if err != nil {
		return nil, err
	}
	return &RsDecoder{
		Config: config,
		output: output,
		rsEnc:  enc,
	}, nil
}

func (d *RsDecoder) AddSymbol(chunkIdx uint32, symbolIdx uint32, data []byte) error {
	v, ok := d.RsChunkDecoders.Load(chunkIdx)
	if !ok {
		v, _ = d.RsChunkDecoders.LoadOrStore(chunkIdx, newRsChunkDecoder(d.Config, d.rsEnc, chunkIdx))
	}
	chunkDec := v.(*rsChunkDecoder)
	
	// Assume symbolIdx is encoded as: (shardIdx << 16) | innerSymbolIdx
	shardIdx := int(symbolIdx >> 16)
	innerIdx := int(symbolIdx & 0xFFFF)

	ready, err := chunkDec.addSymbol(shardIdx, innerIdx, data)
	if err != nil {
		return err
	}

	
	if ready {
		decodedData, err := chunkDec.decode()
		if err != nil {
			return fmt.Errorf("decode chunk %d failed: %v", chunkIdx, err)
		}

		offset := int64(chunkIdx) * int64(d.Config.ChunkSize)
		d.output.OnDecodedData(decodedData, offset, chunkIdx)


		log.Printf("Chunk %d decoded successfully", chunkIdx)
		d.RsChunkDecoders.Delete(chunkIdx)
	} 

	return nil
}

func (d *RsDecoder) Close() error {
	return nil
}

type rsChunkDecoder struct {
	mu     sync.Mutex
	config DecoderConfig
	enc    rs.Encoder

	chunkIdx uint32
	shards   [][]byte
	// Track received symbols to avoid duplicates counting towards completion
	// shardsReceived[shardIdx][symbolIdx] = true
	shardsReceived []map[int]struct{}
	fullShardCnt   int
	decoded        bool
}

func newRsChunkDecoder(config DecoderConfig, enc rs.Encoder, chunkIdx uint32) *rsChunkDecoder {
	totalShards := int(config.DataShards + config.ParityShards)
	shardSize := (int(config.ChunkSize) + int(config.DataShards) - 1) / int(config.DataShards)

	shards := make([][]byte, totalShards)
	for i := range shards {
		shards[i] = make([]byte, shardSize)
	}

	shardsReceived := make([]map[int]struct{}, totalShards)
	for i := range shardsReceived {
		shardsReceived[i] = make(map[int]struct{})
	}

	return &rsChunkDecoder{
		config:         config,
		enc:            enc,
		chunkIdx:       chunkIdx,
		shards:         shards,
		shardsReceived: shardsReceived,
	}
}

func (c *rsChunkDecoder) addSymbol(shardIdx int, innerIdx int, data []byte) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.decoded {
		return false, nil
	}

	if shardIdx >= len(c.shards) {
		return false, fmt.Errorf("invalid shard index %d", shardIdx)
	}

	shard := c.shards[shardIdx]
	offset := innerIdx * int(c.config.SymbolSize)

	if offset >= len(shard) {
		return false, nil // Ignore out of bounds
	}

	copy(shard[offset:], data)
	data = nil 
	// Track unique symbols
	if _, exists := c.shardsReceived[shardIdx][innerIdx]; !exists {
		c.shardsReceived[shardIdx][innerIdx] = struct{}{}

		// Check if shard is complete
		shardSize := len(shard)
		symSize := int(c.config.SymbolSize)
		totalSyms := (shardSize + symSize - 1) / symSize

		if len(c.shardsReceived[shardIdx]) == totalSyms {
			c.fullShardCnt++
		}
	}

	if c.fullShardCnt >= int(c.config.DataShards) {
		return true, nil
	}

	return false, nil
}

func (c *rsChunkDecoder) decode() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.decoded {
		return nil, nil
	}

	inputShards := make([][]byte, len(c.shards))
	shardSize := len(c.shards[0])
	symSize := int(c.config.SymbolSize)
	totalSyms := (shardSize + symSize - 1) / symSize

	for i := range c.shards {
		if len(c.shardsReceived[i]) == totalSyms {
			inputShards[i] = c.shards[i]
		} else {
			inputShards[i] = nil
		}
	}

	if err := c.enc.Reconstruct(inputShards); err != nil {
		return nil, err
	}

	// Calculate actual data size for this chunk
	chunkSize := int64(c.config.ChunkSize)
	fileSize := int64(c.config.FileSize)
	startOffset := int64(c.chunkIdx) * chunkSize
	if startOffset+chunkSize > fileSize {
		chunkSize = fileSize - startOffset
	}

	var buf bytes.Buffer
	if err := c.enc.Join(&buf, inputShards, int(chunkSize)); err != nil {
		return nil, err
	}

	c.decoded = true
	return buf.Bytes(), nil
}
