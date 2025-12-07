package encoder

import (
	"context"
	"fmt"
)

// NcEncoder 无编码器
// 功能说明：
//   实现无前向纠错编码的直接传输
// 特性：
//   - 零冗余开销
//   - 适用于高可靠性网络
//   - 简单的分片传输
// 性能特点：
//   1. 编码/解码零延迟
//   2. 零冗余开销
//   3. 内存占用最小
type NcEncoder struct {
	Config   EncoderConfig
	Callback SendCallback
}

func NewNcEncoder(config EncoderConfig) (*NcEncoder, error) {
	return &NcEncoder{
		Config: config,
	}, nil
}

// Encode 编码方法实现
// 功能说明：
//   对分块数据进行无编码处理，将数据切分为符号
// 算法步骤：
//   1. 遍历所有数据块
//   2. 对每个块按符号大小切片
//   3. 调用回调函数发送每个符号
// 参数：
//   ctx       - 上下文，支持取消操作
//   chunkCount - 总块数
//   provider  - 数据提供函数
//   cb        - 符号发送回调函数
// 返回值：
//   error - 编码过程中的错误
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
//   设置符号发送回调函数
// 参数：
//   cb - 发送回调函数
func (e *NcEncoder) SetCallback(cb SendCallback) {
	e.Callback = cb
}

func (e *NcEncoder) Close() error {
	return nil
}
