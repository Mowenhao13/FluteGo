package decoder

import (
	"fmt"
	"sync"
)

type NcDecoder struct {
	Config DecoderConfig
	output OutputHandler
	chunks sync.Map // map[chunkIdx]*ncChunkState
}

type ncChunkState struct {
	mu        sync.Mutex
	data      []byte
	received  map[int]struct{}
	got       int
	expected  int
	completed bool
}

func (d *NcDecoder) calChunkSize(chunkIdx uint32) uint32 {
	startOffset := uint64(chunkIdx) * uint64(d.Config.ChunkSize)
	if startOffset >= d.Config.FileSize {
		return 0
	}

	endOffset := startOffset + uint64(d.Config.ChunkSize)
	if endOffset > d.Config.FileSize {
		return uint32(d.Config.FileSize - startOffset)
	}
	return d.Config.ChunkSize
}

func NewNcDecoder(config DecoderConfig, output OutputHandler) (*NcDecoder, error) {
	if config.ChunkSize > 1024*1024*1024 { // 最大1GB per chunk
		return nil, fmt.Errorf("chunk大小超过限制: %d", config.ChunkSize)
	}

	return &NcDecoder{
		Config: config,
		output: output,
	}, nil
}

func (d *NcDecoder) AddSymbol(chunkIdx uint32, symbolIdx uint32, data []byte) error {
	// Calculate symbol and chunk sizes
	symbolSize := int(d.Config.SymbolSize)
	if symbolSize <= 0 {
		return fmt.Errorf("invalid symbol size: %d", d.Config.SymbolSize)
	}

	chunkSize := int(d.calChunkSize(chunkIdx))
	if chunkSize == 0 {
		// nothing to do (out of range)
		return nil
	}

	// symbolIdx represents the symbol index within this chunk
	symIdx := int(symbolIdx)

	// get or create chunk state
	v, _ := d.chunks.LoadOrStore(chunkIdx, &ncChunkState{
		data:     make([]byte, chunkSize),
		received: make(map[int]struct{}, (chunkSize+symbolSize-1)/symbolSize),
		expected: (chunkSize + symbolSize - 1) / symbolSize,
	})
	st := v.(*ncChunkState)

	st.mu.Lock()
	defer st.mu.Unlock()

	if st.completed {
		return nil
	}

	// compute write offset for this symbol
	off := symIdx * symbolSize
	if off >= len(st.data) {
		// symbol index out of range for this chunk
		return nil
	}

	// avoid double-counting the same symbol
	if _, ok := st.received[symIdx]; ok {
		return nil
	}

	// copy payload (may be last symbol shorter than symbolSize)
	maxCopy := len(st.data) - off
	if maxCopy > len(data) {
		maxCopy = len(data)
	}
	copy(st.data[off:off+maxCopy], data[:maxCopy])
	st.received[symIdx] = struct{}{}
	st.got++

	// if we have collected enough symbols, mark complete and flush
	if st.got >= st.expected {
		st.completed = true
		// make a copy of the exact-sized chunk because downstream may retain the slice
		out := make([]byte, chunkSize)
		copy(out, st.data)

		// delete from map to free memory
		d.chunks.Delete(chunkIdx)

		// calculate offset in file
		offset := int64(chunkIdx) * int64(d.Config.ChunkSize)
		if err := d.output.OnDecodedData(out, offset, chunkIdx); err != nil {
			return err
		}
	}

	data = nil 

	return nil
}


func (d *NcDecoder) Close() error {
	return nil
}