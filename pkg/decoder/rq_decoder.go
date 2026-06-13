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
//
// # 工作方式
//
// 维护多个 chunk decoder 实例，对应当前正在恢复的 chunk，支持异步接收符号、判断齐全并回调输出。
type RqDecoder struct {
	Config          DecoderConfig
	output          OutputHandler
	RqChunkDecoders *shard_map.ShardedMap
	DecoderCnt      uint32 // 实际最大到 uint16(65535) 支持解码最大文件尺寸: 65535 * chunkSize
	engine          *raptorq.RaptorQ
}

// rqChunkDecoder 负责一块 chunk 的符号收集和解码。
//
// # 字段
//
//   - `decoder`: RaptorQ 解码器实例。
//   - `received`: 已接收符号数量。
//   - `expected`: 期望符号数量。
//   - `chunkSize`: 本 chunk 的实际字节长度。
//   - `startSeq`: 起始序列号。
//   - `decoded`: 是否已经解码完成。
//   - `lastUsed`: 上次接收时间。
//   - `mutex`: 同步多 goroutine 访问。
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

// calRequiredSymbols 根据 chunk 大小和冗余比例估算所需符号数量。
//
// # 参数
//
//   - `actualChunkSize`: 当前 chunk 实际大小。
//
// # 返回值
//
//	需要接收的符号数量（uint16）。
func (r *RqDecoder) calRequiredSymbols(actualChunkSize uint32) uint16 {
	baseSymbols := uint16((actualChunkSize + uint32(r.Config.SymbolSize) - 1) / uint32(r.Config.SymbolSize))
	return baseSymbols
}

// max chunkSize = 4096 MB
// calChunkSize 计算 chunk 索引对应的实际字节长度，最后一个 chunk 可能小于固定大小。
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

// getrqChunkDecoder 获取或初始化某个 chunk 对应的 RaptorQ 解码器。
//
// # 参数
//
//   - `chunkIdx`: chunk 索引。
//
// # 错误
//
//   - chunk 超出文件范围。
//   - chunk 已经解码完成。
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

// NewRqDecoder 初始化一个支持 sliding window 的 RaptorQ 解码器。
//
// # 参数
//
//   - `config`: 解码上下文配置。
//   - `output`: chunk 解码完成后的回调。
//
// # 返回值
//
//	返回构建好的 `RqDecoder` 实例与可能的错误。
func NewRqDecoder(config DecoderConfig, output OutputHandler) (*RqDecoder, error) {
	if config.ChunkSize > 1024*1024*1024 { // 最大1GB per chunk
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

// AddSymbol 接收一个符号，填充相应 chunk 并尝试触发解码。
//
// 成功解码后会通过 OutputHandler 回调完整 chunk 数据。
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

			// 注意：不删除已解码完成 decoder，保留 decoded=true 标记
			atomic.AddUint32(&r.DecoderCnt, ^uint32(0))
		} else {
			log.Printf("chunk %d 解码尝试失败，继续接收符号...", chunkIdx)
		}
	}

	return nil
}

// 清理已解码的 decoder（供 Receiver 调用）
// CleanupDecoded 清理已解码或闲置过久的 chunk。
//
// # 返回值
//
//	清理的 chunk 数量。
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

// 获取解码统计信息
// GetStats 获取当前 chunk 解码统计信息。
//
// # 返回值
//
//	`decoded`: 已完成解码的 chunk 数
//	`total`: 当前活动 chunk 总数
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

// SetFileSize 更新解码器感知到的文件总大小。
func (r *RqDecoder) SetFileSize(fileSize uint64) {
	r.Config.FileSize = fileSize
}

// Close 释放 RqDecoder 关联的资源。
func (r *RqDecoder) Close() error {
	r.RqChunkDecoders.Range(func(key uint32, value interface{}) bool {
		if dec := value.(*rqChunkDecoder); dec.decoder != nil {
			// 如果有释放方法则调用
			dec.decoder = nil
		}
		return true
	})
	return nil
}

// Decode 目前为占位实现，RaptorQ 解码在 AddSymbol 中实时完成。
func (r *RqDecoder) Decode() error {
	// RaptorQ 解码是增量进行的，在 AddSymbol 中完成解码逻辑
	return nil
}
