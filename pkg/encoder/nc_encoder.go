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
	"math"
)

// NcEncoder 无编码器
// 功能说明：
//
//	实现无前向纠错编码的直接传输
//
// 特性：
//   - 零冗余开销
//   - 适用于高可靠性网络
//   - 窗口化符号交织发送，分散突发丢包
//
// 性能特点：
//  1. 编码/解码零延迟
//  2. 零冗余开销
//  3. 内存占用最小
type NcEncoder struct {
	Config   EncoderConfig
	Callback SendCallback
}

// NewNcEncoder 创建一个用于直接传输的 NoCode 编码器。
//
// # 描述
//
// 该编码器不执行任何冗余操作，适用于网络丢包率极低的场景。它只负责按符号大小切分数据并交由发送回调。
//
// # 参数
//
//   - `config`: `EncoderConfig`
//     描述分块大小、符号长度及文件路径等基本信息。
//
// # 返回值
//
//	返回初始化后的 `NcEncoder` 和可能出现的初始化错误。
func NewNcEncoder(config EncoderConfig) (*NcEncoder, error) {
	return &NcEncoder{
		Config: config,
	}, nil
}

// Encode 编码方法实现
// 功能说明：
//
//	对分块数据进行无编码处理，将数据切分为符号
//
// 算法步骤：
//  1. 遍历所有数据块
//  2. 对每个块按符号大小切片
//  3. 调用回调函数发送每个符号
//
// 参数：
//
//	ctx       - 上下文，支持取消操作
//	chunkCount - 总块数
//	provider  - 数据提供函数
//	cb        - 符号发送回调函数
//
// 返回值：
//
//	error - 编码过程中的错误
//
// Encode 以滑动窗口方式遍历所有 chunk，窗口内按符号 ID 交织发送。
//
// 与 RaptorQ 编码器保持一致的发送模式：
//   - 窗口大小由 constant.WindowsSize 控制
//   - 窗口内先发送所有 chunk 的 sym0，再发送所有 chunk 的 sym1，...
//   - 这样突发丢包会分散到不同 chunk，而非集中在一个 chunk
//
// # 参数
//
//   - `ctx`: `context.Context`
//     可选上下文，用于在网络中断或取消时立即终止发送。
//   - `chunkCount`: `uint32`
//     当前文件被拆分的 chunk 数量。
//   - `provider`: `DataProvider`
//     提供 chunk 原始数据的回调。
//   - `cb`: `SendCallback`
//     实际发送符号的回调函数。
//
// # 返回值
//
//	成功完成时返回 `nil`，否则返回第一处发生的错误。
func (e *NcEncoder) Encode(ctx context.Context, chunkCount uint32, provider DataProvider, cb SendCallback) error {
	callback := cb
	if callback == nil {
		callback = e.Callback
	}

	symbolSize := int(e.Config.SymbolSize)
	windowSize := uint32(constant.WindowsSize)

	// 滑动窗口处理
	for startChunk := uint32(0); startChunk < chunkCount; startChunk += windowSize {
		endChunk := startChunk + windowSize
		if endChunk > chunkCount {
			endChunk = chunkCount
		}

		currentBatchSize := endChunk - startChunk

		// 1. 加载当前窗口内所有 chunk 的数据
		type chunkData struct {
			idx     uint32
			data    []byte
			size    int
			numSyms int
		}
		chunks := make([]*chunkData, currentBatchSize)
		maxSymbols := 0

		for i := uint32(0); i < currentBatchSize; i++ {
			chunkIdx := startChunk + i
			data, sz, err := provider(chunkIdx)
			if err != nil {
				return fmt.Errorf("failed to get data for chunk %d: %w", chunkIdx, err)
			}
			numSyms := (sz + symbolSize - 1) / symbolSize
			if numSyms > maxSymbols {
				maxSymbols = numSyms
			}
			chunks[i] = &chunkData{
				idx:     chunkIdx,
				data:    data,
				size:    sz,
				numSyms: numSyms,
			}
		}

		// 2. 按符号 ID 交织发送源符号：先所有 chunk 的 sym0，再所有 chunk 的 sym1，...
		for symID := 0; symID < maxSymbols; symID++ {
			if ctx != nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
			}

			for _, c := range chunks {
				if symID >= c.numSyms {
					continue
				}

				start := symID * symbolSize
				end := start + symbolSize
				if end > c.size {
					end = c.size
				}
				symbol := c.data[start:end]

				if err := callback(c.idx, uint32(symID), uint32(c.size), symbol); err != nil {
					return fmt.Errorf("callback failed for chunk %d symbol %d: %w", c.idx, symID, err)
				}
			}
		}

		// 3. 发送冗余符号：循环重发源符号
		// NoCode 没有编码能力，但可以通过循环重发原始符号提供冗余。
		// 冗余符号的 symID = symID % numSyms（与源符号相同），
		// 接收端 nc_decoder 已有去重逻辑（received[symIdx]），重复包会被忽略。
		// 如果源符号丢了，冗余重发会补上。
		if e.Config.RedundancyRatio > 1.0 {
			// 计算每个 chunk 需要发送的总符号数（含冗余）
			// 取窗口内最大 numSyms 计算，确保所有 chunk 都有足够冗余
			if maxSymbols > 0 {
				totalSymbols := int(math.Ceil(float64(maxSymbols) * e.Config.RedundancyRatio))
				for symID := maxSymbols; symID < totalSymbols; symID++ {
					if ctx != nil {
						select {
						case <-ctx.Done():
							return ctx.Err()
						default:
						}
					}

					for _, c := range chunks {
						if c.numSyms == 0 {
							continue
						}
						// 循环重发：symID 映射回原始符号索引
						origSymID := symID % c.numSyms
						start := origSymID * symbolSize
						end := start + symbolSize
						if end > c.size {
							end = c.size
						}
						symbol := c.data[start:end]

						if err := callback(c.idx, uint32(origSymID), uint32(c.size), symbol); err != nil {
							// 冗余符号发送失败不阻塞整体流程
							return fmt.Errorf("callback failed for chunk %d repair symbol %d: %w", c.idx, symID, err)
						}
					}
				}
			}
		}

		// 4. 释放当前窗口的数据引用，帮助 GC
		for i := range chunks {
			chunks[i].data = nil
			chunks[i] = nil
		}
		chunks = nil
	}

	return nil
}

// SetCallback 设置回调函数
// 功能说明：
//
//	设置符号发送回调函数
//
// 参数：
//
//	cb - 发送回调函数
//
// SetCallback 注册默认的符号发送回调。
//
// # 参数
//
//   - `cb`: `SendCallback`
//     用于在后续 `Encode` 调用中发送符号。
func (e *NcEncoder) SetCallback(cb SendCallback) {
	e.Callback = cb
}

func (e *NcEncoder) Close() error {
	// NoCode 模式无额外资源需要释放
	return nil
}
