package encoder

import (
	constant "FluteGo/constant"
	"context"
	"fmt"
	"log"

	rs "github.com/klauspost/reedsolomon"
)

type RsEncoder struct {
	Config EncoderConfig
	RsExtraParam
	Callback SendCallback
}

type RsExtraParam struct {
	// SIMD指令集优化（性能从低到高）
	WithSSE2    bool // SSE2指令集（x86基础）
	WithSSSE3   bool // SSSE3指令集
	WithAVX2    bool // AVX2指令集（推荐，现代CPU）
	WithAVX512  bool // AVX512指令集（服务器级CPU）
	WithAVXGFNI bool // AVX+GFNI指令集（最新Intel）
	WithGFNI    bool // GFNI指令集（Galois Field新指令）

	// 并发控制
	WithConcurrentStreamReads  bool // 并发读取流
	WithConcurrentStreamWrites bool // 并发写入流
	WithConcurrentStreams      bool // 同时启用读写并发

	// 高级编码技术
	WithLeopardGF      bool // Leopard GF算法（大分片优化）
	WithLeopardGF16    bool // Leopard GF(2^16)算法
	WithInversionCache bool // 逆矩阵缓存（多次解码优化）
}

func loadExtraParams() RsExtraParam {
	return RsExtraParam{
		WithSSE2:    constant.RsWithSSE2,
		WithSSSE3:   constant.RsWithSSSE3,
		WithAVX2:    constant.RsWithAVX2,
		WithAVX512:  constant.RsWithAVX512,
		WithAVXGFNI: constant.RsWithAVXGFNI,
		WithGFNI:    constant.RsWithGFNI,

		WithConcurrentStreamReads:  constant.RsWithConcurrentStreamReads,
		WithConcurrentStreamWrites: constant.RsWithConcurrentStreamWrites,
		WithConcurrentStreams:      constant.RsWithConcurrentStreams,

		WithLeopardGF:      constant.RsWithLeopardGF,
		WithLeopardGF16:    constant.RsWithLeopardGF16,
		WithInversionCache: constant.RsWithInversionCache,
	}
}

func NewRsEncoder(config EncoderConfig) (*RsEncoder, error) {
	rsExtraParam := loadExtraParams()
	return &RsEncoder{
		Config:       config,
		RsExtraParam: rsExtraParam,
	}, nil
}

func (e *RsEncoder) EncodeChunk(chunkIdx uint32, chunkSz uint32, data []byte, cb SendCallback) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	callback := cb
	if callback == nil {
		callback = e.Callback
	}

	dataShards := int(e.Config.DataShards)
	parityShards := int(e.Config.ParityShards)
	totalShards := dataShards + parityShards

	enc, err := rs.New(dataShards, parityShards,
		rs.WithSSE2(e.RsExtraParam.WithSSE2),
		rs.WithSSSE3(e.RsExtraParam.WithSSSE3),
		rs.WithAVX2(e.RsExtraParam.WithAVX2),
		rs.WithAVX512(e.RsExtraParam.WithAVX512),
		rs.WithAVXGFNI(e.RsExtraParam.WithAVXGFNI),
		rs.WithGFNI(e.RsExtraParam.WithGFNI),
		rs.WithConcurrentStreamReads(e.RsExtraParam.WithConcurrentStreamReads),
		rs.WithConcurrentStreamWrites(e.RsExtraParam.WithConcurrentStreamWrites),
		rs.WithConcurrentStreams(e.RsExtraParam.WithConcurrentStreams),
		rs.WithLeopardGF(e.RsExtraParam.WithLeopardGF),
		rs.WithLeopardGF16(e.RsExtraParam.WithLeopardGF16),
		rs.WithInversionCache(e.RsExtraParam.WithInversionCache),
	)

	if err != nil {
		return 0, fmt.Errorf("failed to create Reed-Solomon encoder: %w", err)
	}

	shards, err := enc.Split(data)
	if err != nil {
		return 0, fmt.Errorf("failed to split data into shards: %w", err)
	}

	shardSize := len(shards[0])
	totalSymbolsPerShard := (shardSize + int(e.Config.SymbolSize) - 1) / int(e.Config.SymbolSize)

	// for shardIdx, shard := range shards {
	// 	for symbolIdx := 0; symbolIdx < totalSymbolsPerShard; symbolIdx++ {
	// 		start := symbolIdx * int(e.Config.SymbolSize)
	// 		end := start + int(e.Config.SymbolSize)
	// 		if end > len(shard) {
	// 			end = len(shard)
	// 		}

	// 		if callback != nil {
	// 			if err := callback(chunkIdx, uint32(shardIdx*totalSymbolsPerShard+symbolIdx), chunkSz, shard[start:end]); err != nil {
	// 				if shardIdx < dataShards {
	// 					return shardIdx*totalSymbolsPerShard + symbolIdx, fmt.Errorf("callback failed for data shard %d symbol %d: %w", shardIdx, symbolIdx, err)
	// 				}
	// 				log.Printf("Warning: failed to send parity shard %d symbol %d for chunk %d: %v\n", shardIdx, symbolIdx, chunkIdx, err)
	// 			}
	// 		}
	// 	}
	// }

	symbolSent := 0
	for symbolIdx := 0; symbolIdx < totalSymbolsPerShard; symbolIdx++ {
		for shardIdx := 0; shardIdx < totalShards; shardIdx++ {
			start := symbolIdx * int(e.Config.SymbolSize)
			end := start + int(e.Config.SymbolSize)
			if end > len(shards[shardIdx]) {
				end = len(shards[shardIdx])
			}
			if start > end {
				continue 
			}

			symbolData := shards[shardIdx][start:end]
			if callback != nil {
				if err := callback(chunkIdx, uint32(shardIdx*totalSymbolsPerShard+symbolIdx), chunkSz, symbolData); err != nil {
					if shardIdx < dataShards {
						return shardIdx*totalSymbolsPerShard + symbolIdx, fmt.Errorf("callback failed for data shard %d symbol %d: %w", shardIdx, symbolIdx, err)
					}
					log.Printf("Warning: failed to send parity shard %d symbol %d for chunk %d: %v\n", shardIdx, symbolIdx, chunkIdx, err)
				}
			}
		}
		symbolSent++
	}
	enc = nil

	return symbolSent, nil
}

func (e *RsEncoder) Encode(ctx context.Context, chunkCount uint32, provider DataProvider, cb SendCallback) error {
	callback := cb 
	if callback == nil {
		callback = e.Callback
	}

	// For RS, we just encode chunks sequentially for now as it doesn't support symbol-level interleaving easily
	// without significant changes to how RS works (it's block based).
	for i := uint32(0); i < chunkCount; i++ {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}

		data, sz, err := provider(i)
		if err != nil {
			return err
		}
		_, err = e.EncodeChunk(i, uint32(sz), data, callback)
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *RsEncoder) SetCallback(cb SendCallback) {
	e.Callback = cb
}

// Close 释放编码器资源
func (e *RsEncoder) Close() error {
	// No specific resources to release for Reed-Solomon encoder
	return nil
}
