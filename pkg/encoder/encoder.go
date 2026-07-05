/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */
/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package encoder

import "context"

// Symbol level send callback function
type SendCallback func(chunkIdx uint32, symbolID uint32, chunkSz uint32, symbolData []byte) error

// DataProvider 数据提供者函数
// 功能说明：
//
//	为指定块索引提供原始数据
//
// 参数说明：
//
//	chunkIdx - 块索引
//
// 返回值：
//
//	[]byte - 块数据
//	int    - 数据实际大小
//	error  - 获取数据时的错误
//
// 使用场景：
//  1. 从文件读取数据
//  2. 从内存缓冲区提供数据
//  3. 从网络流中获取数据
type DataProvider func(chunkIdx uint32) ([]byte, int, error)

// EncoderType 编码器类型枚举
// 功能说明：
//
//	定义前向纠错编码算法的类型
//
// 编码器类型：
//
//	EncoderNoCode      - 无编码，直接传输原始数据
//	EncoderRaptorQ     - RaptorQ编码
//	EncoderReedSolomon - Reed-Solomon编码
type EncoderType uint8

const (
	EncoderNoCode EncoderType = iota
	EncoderRaptorQ
	EncoderReedSolomon
)

// EncoderConfig 编码器配置
// 功能说明：
//
//	定义编码器的所有配置参数
//
// 核心字段：
//
//	Type            - 编码器类型
//	ChunkSize      - 每个 source block 包含的最大 symbol 数量
//	SymbolSize     - 符号大小（字节）
//	DataShards     - 数据分片数，Reed-Solomon使用
//	ParityShards   - 校验分片数，Reed-Solomon使用
//	RedundancyRatio - 冗余比例（>1.0）
//	MaxPacketSize  - 最大数据包大小（字节）
//	FileSize       - 文件大小（字节）
//	Fd             - 文件描述符
//	FName          - 发送文件完整路径
type EncoderConfig struct {
	Type            EncoderType
	ChunkSize       uint32  // 每个 source block 包含的最大 symbol 数量
	SymbolSize      uint16  // 符号大小
	DataShards      uint16  // 数据分片数（ReedSolomon）
	ParityShards    uint16  // 校验分片数（ReedSolomon）
	RedundancyRatio float64 // 冗余比例
	MaxPacketSize   uint16  // 最大数据包大小
	FileSize        uint64
	Fd              int
	FName           string
}

// ChunkInfo 块信息结构
// 功能说明：
//
//	描述一个数据块的元数据信息
//
// 字段说明：
//
//	Index    - 块索引，从0开始
//	StartPos - 块在文件中的起始位置
//	Size     - 块的实际大小
//
// 使用场景：
//  1. 文件分块处理
//  2. 块级错误恢复
//  3. 并行处理控制
type ChunkInfo struct {
	Index    uint32
	StartPos uint64
	Size     uint32
}

// BaseEncoder 编码器基础接口
// 功能说明：
//
//	所有具体编码器（RS, RaptorQ, NoCode）必须实现的接口
//
// 核心方法：
//
//	Encode - 编码多个块的交织数据
//	SetCallback - 设置发送回调函数
//	Close - 释放编码器资源
//
// 设计模式：
//
//	策略模式，允许运行时切换编码算法
//
// 线程安全：
//
//	实现应保证多协程安全调用
type BaseEncoder interface {
	// EncodeChunk(chunkIdx uint32, chunkSz uint32, data []byte, cb SendCallback) (int, error)

	// EncodeInterleaved encodes multiple chunks in an interleaved fashion
	Encode(ctx context.Context, chunkCount uint32, provider DataProvider, cb SendCallback) error

	SetCallback(cb SendCallback)

	// Close 释放编码器资源
	Close() error
}

// SendCallback 发送回调函数
// 功能说明：
//
//	当编码器生成一个符号时调用，用于将符号发送到网络
//
// 参数说明：
//
//	chunkIdx   - 块索引
//	symbolID   - 符号标识符
//	chunkSz    - 块大小
//	symbolData - 符号数据
//
// 返回值：
//
//	error - 发送过程中的错误
//
// 使用场景：
//  1. 网络数据包发送
//  2. 数据写入缓冲区
//  3. 统计信息收集
//
// 注意事项：
//
//	回调函数应是非阻塞的，避免阻塞编码器
//
// NewChunkSendCallback 创建一个能够响应 context 取消信号的发送回调包装器。
//
// # 参数
//
//   - `ctx`: `context.Context`
//     若非 `nil`，在其被取消后将立即停止发送，保护编码器不被阻塞。
//   - `handler`: `func(chunkIdx uint32, symbolID uint32, chunkSz uint32, symbolData []byte) error`
//     负责将已编码的符号真正发送到网络或写入缓存中。
//
// # 返回值
//
//	包装后的 `SendCallback`，在上下文取消时优雅退化并返回 `ctx.Err()`。
func NewChunkSendCallback(ctx context.Context, handler func(chunkIdx uint32, symbolID uint32, chunkSz uint32, symbolData []byte) error) SendCallback {
	return func(chunkIdx uint32, symbolID uint32, chunkSz uint32, symbolData []byte) error {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		return handler(chunkIdx, symbolID, chunkSz, symbolData)
	}
}

// NewEncoder 基于配置构建对应的前向纠错编码器实例。
//
// # 参数
//
//   - `config`: `EncoderConfig`
//     描述了当前文件的分块、冗余、符号大小等核心信息。
//
// # 返回值
//
//	返回 `BaseEncoder` 接口实现及初始化过程中可能发生的错误。
//
// # 错误
//
//   - 参数值不合理时会返回特定错误。
//   - 初始化底层编码器（RaptorQ/RS/NoCode）失败时也会原样透传。
func NewEncoder(config EncoderConfig) (BaseEncoder, error) {
	switch config.Type {
	case EncoderRaptorQ:
		enc, err := NewRqEncoder(config)
		if err != nil {
			return nil, err
		}
		return enc, nil
	case EncoderReedSolomon:
		enc, err := NewRsEncoder(config)
		if err != nil {
			return nil, err
		}
		return enc, nil
	case EncoderNoCode:
		enc, err := NewNcEncoder(config)
		if err != nil {
			return nil, err
		}
		return enc, nil
	default:
		return nil, nil
	}
}
