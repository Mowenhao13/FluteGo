package fec

type FecShard interface {
	Data() []byte
	ESI() uint32
}

type DataFecShard struct {
	Shard []byte
	Index uint32
}

func (d *DataFecShard) Data() []byte {
	return d.Shard
}

func (d *DataFecShard) ESI() uint32 {
	return d.Index
}

func NewDataFecShard(shard []byte, index uint32) *DataFecShard {
	return &DataFecShard{
		Shard: shard,
		Index: index,
	}
}

type FecEncoder interface {
	Encode(data []byte) ([]FecShard, error)
}

type FecDecoder interface {
	PushSymbol(encodingSymbol []byte, esi uint32)
	CanDecode() bool
	Decode() bool
	SourceBlock() ([]byte, error)
}

// TODO: impl std::fmt::Debug for dyn FecEncoder

// TODO: impl std::fmt::Debug for dyn FecDecoder
