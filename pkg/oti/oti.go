package oti

type Oti struct {
	FECEncodingID    uint8
	FECInstanceID    uint16
	MaximumChunkSize uint32
	SymbolSize       uint16
	DataShards       uint8
	ParityShards     uint8
}

func NewNoCode(SymbolSize uint16) Oti {
	return Oti{
		FECEncodingID:    0,
		FECInstanceID:    0,
		SymbolSize:       SymbolSize,
		MaximumChunkSize: 0,
	}
}

func NewRaptorQ(SymbolSize uint16) Oti {
	return Oti{
		FECEncodingID:    1,
		FECInstanceID:    1,
		SymbolSize:       SymbolSize,
		MaximumChunkSize: 0,
	}
}

func NewReedSolomon(dataShards, parityShards uint8) Oti {
	return Oti{
		FECEncodingID: 2,
		FECInstanceID: 2,
		DataShards:    dataShards,
		ParityShards:  parityShards,
		// Reed-Solomon 使用分块而非固定符号长度，但仍需设置非零值避免除零
		// 这个值在 RS 模式下实际不使用，仅用于防止除零错误
		SymbolSize: 1,
	}
}
