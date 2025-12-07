package encoder

import (
	"FluteGo/constant"
	"context"
	"fmt"
	"math"

	raptorq "github.com/xssnick/raptorq"
)

// RqEncoder RaptorQ编码器
// 功能说明：
//   实现RaptorQ前向纠错编码
// 算法特点：
//   - 支持任意丢包率
//   - 可生成任意数量冗余符号
//   - 计算复杂度适中
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

func NewRqEncoder(config EncoderConfig) (*RqEncoder, error) {
	return &RqEncoder{
		Config: config,
	}, nil
}

// Encode 编码实现
// 功能说明：
//   对多个块进行RaptorQ编码，支持滑动窗口
// 算法特点：
//   1. 窗口化处理，控制内存使用
//   2. 符号级交织，提高网络适应性
//   3. 支持冗余控制
// 参数：
//   ctx        - 上下文，支持取消
//   chunkCount - 总块数
//   provider   - 数据提供函数
//   cb         - 发送回调函数
// 返回值：
//   error - 编码过程中的错误
// 实现要点：
//   1. 滑动窗口控制内存
//   2. 符号交织抵抗突发丢包
//   3. 错误处理和资源清理
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

		// 2. 在当前窗口内进行交错发送
		for symID := uint32(0); symID < maxTotalSymbols; symID++ {
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
					// 仅对基础符号报错，冗余符号失败则忽略
					if symID < blk.baseSymbols {
						return fmt.Errorf("callback failed for chunk %d symbol %d: %w", blk.id, symID, err)
					}
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

func (e *RqEncoder) SetCallback(cb SendCallback) {
	e.Callback = cb
}

func (e *RqEncoder) Close() error {
	return nil
}
