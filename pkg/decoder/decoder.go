package decoder

import (
	"time"
)

// Chunk level save callback function
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

func NewDecoder(config DecoderConfig, output OutputHandler) (BaseDecoder, error) {
	switch config.Type {
	case DecoderRaptorQ:
		dec, err := NewRqDecoder(config, output)
		if err != nil {
			return nil, err
		}
		return dec, nil
	case DecoderReedSolomon:
		dec, err := NewRsDecoder(config)
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
