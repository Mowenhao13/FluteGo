/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */
package encoder

import (
	"FluteGo/constant"
	"context"
	"fmt"
	"log"
	"math"

	raptorq "github.com/xssnick/raptorq"
)

// RqEncoder RaptorQ编码器
// 功能说明：
//
//	实现RaptorQ前向纠错编码
//
// 算法特点：
//   - 支持任意丢包率
//   - 可生成任意数量冗余符号
//   - 计算复杂度适中
//
// RqEncoder 封装了 RaptorQ 编码的高阶逻辑。
//
// # 描述
//
// 使用滑动窗口策略对多个 chunk 生成符号，支撑高冗余配置与延迟控制。
type RqEncoder struct {
	Config   EncoderConfig
	Callback SendCallback
}

type activeBlock struct {
	id           uint32
	encoder      *raptorq.Encoder
	baseSymbols  uint32
	totalSymbols uint32
	chunkSize    uint32
}

// NewRqEncoder 创建一个 RaptorQ 编码器实例。
//
// # 参数
//
//   - `config`: `EncoderConfig`
//     指定 chunk 大小、冗余比例和符号长度等信息。
//
// # 返回值
//
//	初始化后的 `RqEncoder` 和可能的错误。
func NewRqEncoder(config EncoderConfig) (*RqEncoder, error) {
	return &RqEncoder{
		Config: config,
	}, nil
}

// Encode 编码实现
// 功能说明：
//
//	对多个块进行RaptorQ编码，支持滑动窗口
//
// 算法特点：
//  1. 窗口化处理，控制内存使用
//  2. 符号级交织，提高网络适应性
//  3. 支持冗余控制
//
// 参数：
//
//	ctx        - 上下文，支持取消
//	chunkCount - 总块数
//	provider   - 数据提供函数
//	cb         - 发送回调函数
//
// 返回值：
//
//	error - 编码过程中的错误
//
// 实现要点：
//  1. 滑动窗口控制内存
//  2. 符号交织抵抗突发丢包
//  3. 错误处理和资源清理
//
// Encode 以滑动窗口方式遍历所有 chunk，依次生成并发送符号。
//
// # 参数
//
//   - `ctx`: `context.Context`
//     支持取消的上下文。
//   - `chunkCount`: `uint32`
//     总 chunk 数。
//   - `provider`: `DataProvider`
//     提供 chunk 原始数据。
//   - `cb`: `SendCallback`
//     发送符号的回调函数。
//
// # 返回值
//
//	编码过程中遇到的错误。
func (e *RqEncoder) Encode(ctx context.Context, chunkCount uint32, provider DataProvider, cb SendCallback) error {
	callback := cb
	if callback == nil {
		callback = e.Callback
	}

	// 窗口大小，可以根据内存情况调整
	windowSize := uint32(constant.WindowsSize)

	rq := raptorq.NewRaptorQ(uint32(e.Config.SymbolSize))

	// 分批次处理（滑动窗口）
	for startChunk := uint32(0); startChunk < chunkCount; startChunk += windowSize {
		endChunk := startChunk + windowSize
		if endChunk > chunkCount {
			endChunk = chunkCount
		}

		currentBatchSize := endChunk - startChunk
		blocks := make([]*activeBlock, currentBatchSize)
		maxBaseSymbols := uint32(0)
		maxTotalSymbols := uint32(0)

		// 1. 初始化当前窗口内的编码器
		for i := uint32(0); i < currentBatchSize; i++ {
			chunkIdx := startChunk + i
			data, sz, err := provider(chunkIdx)
			if err != nil {
				return fmt.Errorf("failed to get data for chunk %d: %w", chunkIdx, err)
			}

			enc, err := rq.CreateEncoder(data)
			if err != nil {
				return fmt.Errorf("failed to create encoder for chunk %d: %w", chunkIdx, err)
			}

			baseSymbols := enc.BaseSymbolsNum()
			if baseSymbols == 0 {
				baseSymbols = 1
			}

			totalSymbols := uint32(math.Ceil(float64(baseSymbols) * e.Config.RedundancyRatio))
			if totalSymbols < baseSymbols {
				totalSymbols = baseSymbols
			}

			if baseSymbols > maxBaseSymbols {
				maxBaseSymbols = baseSymbols
			}
			if totalSymbols > maxTotalSymbols {
				maxTotalSymbols = totalSymbols
			}

			blocks[i] = &activeBlock{
				id:           chunkIdx,
				encoder:      enc,
				baseSymbols:  baseSymbols,
				totalSymbols: totalSymbols,
				chunkSize:    uint32(sz),
			}
		}

		// 2. 分两阶段发送：先发送所有源符号，再发送冗余符号
		//    利用 RaptorQ 系统模式特性：
		//    - 源符号 (id < K): 直接返回原始数据，接收方可直接使用无需解码
		//    - 冗余符号 (id >= K): 编码生成的修复符号，用于丢包恢复

		// 阶段 1: 发送所有 chunk 的源符号（优先级最高）
		for symID := uint32(0); symID < maxBaseSymbols; symID++ {
			if ctx != nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
			}

			for _, blk := range blocks {
				if symID >= blk.baseSymbols {
					continue
				}

				symbol := blk.encoder.GenSymbol(symID)
				if symbol == nil {
					continue
				}

				if err := callback(blk.id, symID, blk.chunkSize, symbol); err != nil {
					return fmt.Errorf("callback failed for chunk %d source symbol %d: %w", blk.id, symID, err)
				}
			}
		}

		// 阶段 2: 发送所有 chunk 的冗余符号（修复符号）
		for symID := maxBaseSymbols; symID < maxTotalSymbols; symID++ {
			if ctx != nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
			}

			for _, blk := range blocks {
				if symID >= blk.totalSymbols {
					continue
				}

				symbol := blk.encoder.GenSymbol(symID)
				if symbol == nil {
					continue
				}

				if err := callback(blk.id, symID, blk.chunkSize, symbol); err != nil {
					// 冗余符号发送失败不阻塞整体流程
					log.Printf("warning: failed to send repair symbol %d for chunk %d: %v", symID, blk.id, err)
				}
			}
		}

		// 3. 显式清理当前窗口的编码器引用，帮助 GC
		for i := range blocks {
			blocks[i].encoder = nil
			blocks[i] = nil
		}
		blocks = nil
	}

	return nil
}

// SetCallback 设置默认的发送回调。
//
// # 参数
//
//   - `cb`: `SendCallback`
//     在后续 `Encode` 步骤中被调用。
func (e *RqEncoder) SetCallback(cb SendCallback) {
	e.Callback = cb
}

// Close 释放 RqEncoder 可能占用的资源。
//
// # 描述
//
// 当前实现仅占位，尚无实际资源需要释放。
func (e *RqEncoder) Close() error {
	return nil
}
