package encoder

import "context"

// Symbol level send callback function
type SendCallback func(chunkIdx uint32, symbolID uint32, chunkSz uint32, symbolData []byte) error

// DataProvider retrieves data for a specific chunk
type DataProvider func(chunkIdx uint32) ([]byte, int, error)

type EncoderType uint8

const (
	EncoderNoCode EncoderType = iota
	EncoderRaptorQ
	EncoderReedSolomon
)

type EncoderConfig struct {
	Type            EncoderType
	ChunkSize       uint32  // 单个chunk最大大小
	SymbolSize      uint16  // 符号大小（RaptorQ/NoCode）
	DataShards      uint16  // 数据分片数（ReedSolomon）
	ParityShards    uint16  // 校验分片数（ReedSolomon）
	RedundancyRatio float64 // 冗余比例
	MaxPacketSize   uint16  // 最大数据包大小
	FileSize        uint64
	Fd              int
}

type ChunkInfo struct {
	Index    uint32
	StartPos uint64
	Size     uint32
}

// BaseEncoder 所有具体编码器(RS, RaptorQ, NoCode)必须实现的接口
type BaseEncoder interface {
	// EncodeChunk(chunkIdx uint32, chunkSz uint32, data []byte, cb SendCallback) (int, error)
	
	// EncodeInterleaved encodes multiple chunks in an interleaved fashion
	Encode(ctx context.Context, chunkCount uint32, provider DataProvider, cb SendCallback) error

	SetCallback(cb SendCallback)

	// Close 释放编码器资源
	Close() error
}

// NewChunkSendCallback wraps a handler with optional context cancellation awareness so all FEC encoders
// can share the same callback creation logic.
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
