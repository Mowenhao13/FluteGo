/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package decoder

import (
	"time"
)

// DecodedChunk 表示解码完成的数据块，用于通过channel传输
type DecodedChunk struct {
	Data     []byte
	Offset   int64
	ChunkIdx uint32
}

// OutputHandler 定义 chunk 级写入回调接口，供解码器在完成一个 chunk 解码后通知上层写入逻辑。
//
// # 描述
//
// 解码器在完全恢复一个 chunk 的所有符号后，会调用该接口的 `OnDecodedData` 方法，
// 以便将 chunk 数据传递给文件写入循环或其他处理者。
//
// # 方法
//
//   - `OnDecodedData(data []byte, offset int64, chunkIdx uint32) error`
//   - `data`: 解码完成后的 chunk 数据
//   - `offset`: 写入文件的起始偏移
//   - `chunkIdx`: chunk 索引
//
// # 返回值
//
//   - `error`: 写入失败时返回具体错误，调用方应当决定是否重试或终止整个传输。
//
// # 参考
//
//	RFC 5052 / RFC 5510 中对解码回调的典型处理方式。
type OutputHandler interface {
	OnDecodedData(data []byte, offset int64, chunkIdx uint32) error
}

type DecoderType uint8

const (
	DecoderNoCode DecoderType = iota
	DecoderRaptorQ
	DecoderReedSolomon
)

type DecoderConfig struct {
	Type            DecoderType
	FileSize        uint64
	ChunkSize       uint32  // 单个chunk最大大小
	SymbolSize      uint16  // 符号大小（RaptorQ/NoCode）
	DataShards      uint16  // 数据分片数（ReedSolomon）
	ParityShards    uint16  // 校验分片数（ReedSolomon）
	RedundancyRatio float64 // 冗余比例
	MaxPacketSize   uint16  // 最大数据包大小
	FName           string  // 中间分片文件路径（RS）或最终输出路径（其他）
	OutputPath      string  // 最终输出文件路径（仅用于 RS，优先于 FName）
}

// ChunkInfo chunk基本信息
type ChunkInfo struct {
	Index    uint32
	StartPos uint64
	Size     uint32
	Decoded  bool
	LastUsed time.Time
}

// BaseDecoder 所有具体解码器(RS, RaptorQ, NoCode)必须实现的接口
type BaseDecoder interface {
	// AddSymbol 向解码器添加一个接收到的符号
	// chunkIdx: 块索引
	// symbolIdx: 符号索引
	// data: 符号数据
	// 返回: 如果该符号触发了该块的解码完成，返回 true；否则返回 false (或 error)
	// 注意：解码器内部判断是否凑齐，如果凑齐则调用 SetCallback 设置的回调函数
	AddSymbol(chunkIdx uint32, symbolIdx uint32, data []byte) error

	// Close 释放解码器资源
	Close() error
}

// NewDecoder 根据配置和输出处理器创建对应的解码器实例。
//
// # 参数
//
//   - `config`: `DecoderConfig`
//     定义了当前文件传输的大小、chunk 尺寸、FEC 参数等关键信息。
//   - `output`: `OutputHandler`
//     解码完成的 chunk 数据将通过该接口回调到写入逻辑中。
//
// # 返回值
//
//	返回一个实现了 `BaseDecoder` 接口的解码器实例，以及创建过程中可能出现的错误。
//
// # 错误
//
//   - 配置参数不合法时可能返回特定错误。
//   - 初始化底层解码器（RaptorQ/RS/NoCode）失败时会返回对应错误。
func NewDecoder(config DecoderConfig, output OutputHandler) (BaseDecoder, error) {
	switch config.Type {
	case DecoderRaptorQ:
		dec, err := NewRqDecoder(config, output)
		if err != nil {
			return nil, err
		}
		return dec, nil
	case DecoderReedSolomon:
		dec, err := NewRsDecoder(config, output)
		if err != nil {
			return nil, err
		}
		return dec, nil
	case DecoderNoCode:
		dec, err := NewNcDecoder(config, output)
		if err != nil {
			return nil, err
		}
		return dec, nil
	default:
		return nil, nil
	}
}

// NewDecoderWithChannel 根据配置和channel创建对应的解码器实例。
// 解码完成的 chunk 数据将发送到 channel 中。
func NewDecoderWithChannel(config DecoderConfig, ch chan<- DecodedChunk) (BaseDecoder, error) {
	// 创建一个适配器，将 channel 包装为 OutputHandler
	handler := &channelOutputHandler{ch: ch}
	return NewDecoder(config, handler)
}

// channelOutputHandler 是 OutputHandler 的 channel 适配器实现
type channelOutputHandler struct {
	ch chan<- DecodedChunk
}

func (h *channelOutputHandler) OnDecodedData(data []byte, offset int64, chunkIdx uint32) error {
	// 创建数据副本，避免内存复用问题
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	h.ch <- DecodedChunk{
		Data:     dataCopy,
		Offset:   offset,
		ChunkIdx: chunkIdx,
	}
	return nil
}
