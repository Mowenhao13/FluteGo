/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */
package decoder

import (
	"fmt"
	"sync"
)

// NcDecoder 实现了 NoCode 解码路径，直接将 chunk 数据写回而不做 FEC 操作。
//
// # 工作原理
//
// 该解码器简单缓冲 chunk 数据，待符号齐全后立即回调输出处理器。适用于网络无需纠错时的场景。
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

// NewNcDecoder 初始化一个用于没有 FEC 的 Decoder。
//
// # 参数
//
//   - `config`: `DecoderConfig`
//     包含 chunk 大小、文件大小等元信息。
//   - `output`: `OutputHandler`
//     chunk 解码完成后的写入回调。
//
// # 返回值
//
//	若参数合法则返回 `NcDecoder` 实例，否则返回错误。
func NewNcDecoder(config DecoderConfig, output OutputHandler) (*NcDecoder, error) {
	if config.ChunkSize > 1024*1024*1024 { // 最大1GB per chunk
		return nil, fmt.Errorf("chunk大小超过限制: %d", config.ChunkSize)
	}

	return &NcDecoder{
		Config: config,
		output: output,
	}, nil
}

// AddSymbol 将一个符号写入 chunk 缓冲并在符号齐全后回调写入逻辑。
//
// # 参数
//
//   - `chunkIdx`, `symbolIdx`：chunk/符号索引，用于定位写入位置。
//   - `data`：该符号的数据。
//
// # 返回值
//
//	返回输出处理器的错误（若有）。
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

// Close 释放 NcDecoder 资源。
//
// # 描述
//
// 当前实现无特殊资源，返回 `nil`。
func (d *NcDecoder) Close() error {
	//TODO:
	return nil
}
