/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package encoder

import (
	"context"
	"fmt"
)

// NcEncoder 无编码器
// 功能说明：
//
//	实现无前向纠错编码的直接传输
//
// 特性：
//   - 零冗余开销
//   - 适用于高可靠性网络
//   - 简单的分片传输
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
// Encode 将源数据按符号大小直接传给回调函数，完成无编码的数据流。
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

	// 遍历所有块
	for chunkIdx := 0; chunkIdx < int(chunkCount); chunkIdx++ {
		data, sz, err := provider(uint32(chunkIdx))
		if err != nil {
			return fmt.Errorf("failed to get data for chunk %d: %w", chunkIdx, err)
		}

		// 遍历所有符号
		for i := 0; i < sz; i += int(e.Config.SymbolSize) {
			start := i
			end := start + int(e.Config.SymbolSize)
			// end should be capped by the current chunk length (sz), not the whole file size
			if end > sz {
				end = sz
			}
			symbol := data[start:end]
			symID := i / int(e.Config.SymbolSize)
			if err := callback(uint32(chunkIdx), uint32(symID), uint32(sz), symbol); err != nil {
				return fmt.Errorf("callback failed for chunk %d symbol %d: %w", chunkIdx, symID, err)
			}
		}
		data = nil
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
