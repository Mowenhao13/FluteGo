/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package decoder

import (
	"FluteGo/pkg/shard_map"
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	raptorq "github.com/xssnick/raptorq"
)

// RqDecoder 封装了 RaptorQ 解码流程及状态。
type RqDecoder struct {
	Config          DecoderConfig
	output          OutputHandler
	RqChunkDecoders *shard_map.ShardedMap
	DecoderCnt      uint32
	RecoveredChunks uint32
	engine          *raptorq.RaptorQ
}

type rqChunkDecoder struct {
	decoder   *raptorq.Decoder
	received  int
	expected  uint16
	chunkSize uint32
	startSeq  uint64
	decoded   bool
	lastUsed  time.Time
	mutex     sync.Mutex
}

func (r *RqDecoder) calRequiredSymbols(actualChunkSize uint32) uint16 {
	baseSymbols := uint16((actualChunkSize + uint32(r.Config.SymbolSize) - 1) / uint32(r.Config.SymbolSize))
	return baseSymbols
}

func (r *RqDecoder) calChunkSize(chunkIdx uint32) uint32 {
	startOffset := uint64(chunkIdx) * uint64(r.Config.ChunkSize)
	if startOffset >= r.Config.FileSize {
		return 0
	}
	endOffset := startOffset + uint64(r.Config.ChunkSize)
	if endOffset > r.Config.FileSize {
		return uint32(r.Config.FileSize - startOffset)
	}
	return r.Config.ChunkSize
}

func (r *RqDecoder) getrqChunkDecoder(chunkIdx uint32) (*rqChunkDecoder, error) {
	actualChunkSize := r.calChunkSize(chunkIdx)
	if actualChunkSize <= 0 {
		return nil, fmt.Errorf("无效的chunk索引: %d", chunkIdx)
	}

	if existing, exists := r.RqChunkDecoders.Load(chunkIdx); exists {
		dec := existing.(*rqChunkDecoder)
		dec.lastUsed = time.Now()
		if dec.decoded {
			return nil, fmt.Errorf("Chunk %d already decoded", chunkIdx)
		}
		return dec, nil
	}

	dec, err := r.engine.CreateDecoder(uint32(actualChunkSize))
	if err != nil {
		return nil, fmt.Errorf("创建decoder失败: %v", err)
	}

	requiredSymbols := r.calRequiredSymbols(actualChunkSize)
	startSeq := uint64(chunkIdx) * uint64(r.Config.ChunkSize)

	newrqChunkDecoder := &rqChunkDecoder{
		decoder:   dec,
		received:  0,
		expected:  requiredSymbols,
		chunkSize: actualChunkSize,
		startSeq:  startSeq,
		lastUsed:  time.Now(),
	}

	existing, loaded := r.RqChunkDecoders.LoadOrStore(chunkIdx, newrqChunkDecoder)
	if loaded {
		if dec, ok := existing.(*rqChunkDecoder); ok && dec != nil {
			dec.lastUsed = time.Now()
			if dec.decoded {
				return nil, fmt.Errorf("chunk %d already decoded", chunkIdx)
			}
			return dec, nil
		}
		r.RqChunkDecoders.Store(chunkIdx, newrqChunkDecoder)
		return newrqChunkDecoder, nil
	}
	atomic.AddUint32(&r.DecoderCnt, 1)

	return newrqChunkDecoder, nil
}

func NewRqDecoder(config DecoderConfig, output OutputHandler) (*RqDecoder, error) {
	if config.ChunkSize > 1024*1024*1024 {
		return nil, fmt.Errorf("chunk大小超过限制: %d", config.ChunkSize)
	}

	engine := raptorq.NewRaptorQ(uint32(config.SymbolSize))

	return &RqDecoder{
		Config:          config,
		output:          output,
		engine:          engine,
		RqChunkDecoders: shard_map.NewShardedMap(),
	}, nil
}

func (r *RqDecoder) AddSymbol(chunkIdx uint32, symbolIdx uint32, data []byte) error {
	dec, err := r.getrqChunkDecoder(chunkIdx)
	if err != nil {
		if err.Error() == "Chunk "+strconv.FormatUint(uint64(chunkIdx), 10)+" already decoded" {
			return nil
		}
		return err
	}

	dec.mutex.Lock()
	defer dec.mutex.Unlock()
	dec.lastUsed = time.Now()

	if dec.decoded {
		return nil
	}

	canTryDecode, err := dec.decoder.AddSymbol(uint32(symbolIdx), data)
	if err != nil {
		return fmt.Errorf("添加符号失败: %v", err)
	}

	dec.received++

	if canTryDecode {
		success, result, err := dec.decoder.Decode()
		if err != nil {
			return fmt.Errorf("解码失败: %v", err)
		}

		if success {
			if dec.received > int(dec.expected) {
				extra := dec.received - int(dec.expected)
				log.Printf("RaptorQ recovery: chunk %d decoded with %d extra symbols (received %d/%d)",
					chunkIdx, extra, dec.received, dec.expected)
				atomic.AddUint32(&r.RecoveredChunks, 1)
			}

			offset := int64(chunkIdx) * int64(r.Config.ChunkSize)
			r.output.OnDecodedData(result, offset, chunkIdx)

			dec.decoded = true
			dec.decoder = nil

			atomic.AddUint32(&r.DecoderCnt, ^uint32(0))
		} else {
			log.Printf("RaptorQ recovery FAILED: chunk %d decode failed (received %d/%d symbols, need >=%d to retry)",
				chunkIdx, dec.received, dec.expected, dec.expected)
		}
	}

	return nil
}

func (r *RqDecoder) CleanupDecoded() int {
	cleaned := 0
	r.RqChunkDecoders.Range(func(key uint32, value interface{}) bool {
		dec := value.(*rqChunkDecoder)
		if dec.decoded || time.Since(dec.lastUsed) > 30*time.Second {
			r.RqChunkDecoders.Delete(key)
			atomic.AddUint32(&r.DecoderCnt, ^uint32(0))
			cleaned++
		}
		return true
	})
	return cleaned
}

func (r *RqDecoder) GetStats() (decoded, total int) {
	r.RqChunkDecoders.Range(func(key uint32, value interface{}) bool {
		total++
		dec := value.(*rqChunkDecoder)
		if dec.decoded {
			decoded++
		}
		return true
	})
	return decoded, total
}

func (r *RqDecoder) SetFileSize(fileSize uint64) {
	r.Config.FileSize = fileSize
}

func (r *RqDecoder) Close() error {
	r.RqChunkDecoders.Range(func(key uint32, value interface{}) bool {
		if dec := value.(*rqChunkDecoder); dec.decoder != nil {
			dec.decoder = nil
		}
		return true
	})
	return nil
}

func (r *RqDecoder) Decode() error {
	return nil
}

func (r *RqDecoder) GetRecoveredCount() uint32 {
	return atomic.LoadUint32(&r.RecoveredChunks)
}
