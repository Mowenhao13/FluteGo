package decoder

import (
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	raptorq "github.com/xssnick/raptorq"
)

type RqDecoder struct {
	Config          DecoderConfig
	output          OutputHandler
	RqChunkDecoders sync.Map
	DecoderCnt      uint32 // 实际最大到 uint16(65535) 支持解码最大文件尺寸: 65535 * chunkSize
	engine          *raptorq.RaptorQ
}

type rqChunkDecoder struct {
	decoder   *raptorq.Decoder
	received  int        // 已接收符号数
	expected  uint16     // 期望接收符号数
	chunkSize uint32     // chunk大小
	startSeq  uint64     // 起始序列号
	decoded   bool       // 是否已解码成功
	lastUsed  time.Time  // 最后使用时间
	mutex     sync.Mutex // 保护decoder状态
}

func (r *RqDecoder) calRequiredSymbols(actualChunkSize uint32) uint16 {
	baseSymbols := uint16((actualChunkSize + uint32(r.Config.SymbolSize) - 1) / uint32(r.Config.SymbolSize))
	return uint16(float64(baseSymbols) * r.Config.RedundancyRatio)
}

// max chunkSize = 4096 MB
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

	// currCnt := atomic.LoadUint32(&r.DecoderCnt)
	// // if chunkIdx > 10240 { // 假设为10G文件
	// // 	fmt.Printf("警告: decoder数量接近限制 (%d)", currCnt)
	// // }

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
		// 安全类型转换
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
	if config.ChunkSize > 1024*1024*1024 { // 最大1GB per chunk
		return nil, fmt.Errorf("chunk大小超过限制: %d", config.ChunkSize)
	}

	engine := raptorq.NewRaptorQ(uint32(config.SymbolSize))

	return &RqDecoder{
		Config: config,
		output: output,
		engine: engine,
	}, nil
}

func (r *RqDecoder) AddSymbol(chunkIdx uint32, symbolIdx uint32, data []byte) error {
	dec, err := r.getrqChunkDecoder(chunkIdx)
	if err != nil {
		if err.Error() == "Chunk "+strconv.FormatUint(uint64(chunkIdx), 10)+" already decoded" {
			// 已经解码成功，正常忽略
			return nil
		}
		return err
	}

	dec.mutex.Lock()
	defer dec.mutex.Unlock()
	dec.lastUsed = time.Now()

	// 解码成功
	if dec.decoded {
		return nil
	}

	// 添加符号到decoder
	canTryDecode, err := dec.decoder.AddSymbol(uint32(symbolIdx), data)
	if err != nil {
		return fmt.Errorf("添加符号失败: %v", err)
	}

	dec.received++
	// if dec.received%200 == 0 && dec.received > 0 {
	// 	log.Printf("chunk: %d: 已接收 %d/%d 个符号", chunkIdx, dec.received, dec.expected)
	// }

	if canTryDecode {
		success, result, err := dec.decoder.Decode()
		if err != nil {
			return fmt.Errorf("解码失败: %v", err)
		}

		if success {
			// log.Printf("chunk %d 解码成功! 接收符号数: %d/%d",
			// 	chunkIdx, dec.received, dec.expected)

			// Offset calculation must use the fixed ChunkSize, not the current chunk's size (which might be smaller for the last chunk)
			offset := int64(chunkIdx) * int64(r.Config.ChunkSize)
			r.output.OnDecodedData(result, offset, chunkIdx)

			dec.decoded = true
			dec.decoder = nil
			// log.Printf("chunk %d 数据已实时写入文件, 偏移量: %d, 大小: %d",
			// 	chunkIdx, int64(chunkIdx)*int64(r.Config.ChunkSize), len(result))

			// 删除已解码完成的 decoder
			r.RqChunkDecoders.Delete(chunkIdx)
			atomic.AddUint32(&r.DecoderCnt, ^uint32(0))
		} else {
			log.Printf("chunk %d 解码尝试失败，继续接收符号...", chunkIdx)
		}
	}

	return nil
}

// 清理已解码的 decoder（供 Receiver 调用）
func (r *RqDecoder) CleanupDecoded() int {
	cleaned := 0
	r.RqChunkDecoders.Range(func(key, value interface{}) bool {
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

// 获取解码统计信息
func (r *RqDecoder) GetStats() (decoded, total int) {
	r.RqChunkDecoders.Range(func(key, value interface{}) bool {
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
	r.RqChunkDecoders.Range(func(key, value interface{}) bool {
		if dec := value.(*rqChunkDecoder); dec.decoder != nil {
			// 如果有释放方法则调用
			dec.decoder = nil
		}
		return true
	})
	return nil
}
