package object

import (
	"Flute_go/pkg/lct"
	t "Flute_go/pkg/type"
)

type Pkt struct {
	Payload           []byte    // 实际传输的数据内容（编码后的符号数据）
	TransferLength    uint64    // 传输对象的总长度（字节）
	Esi               uint32    // Encoding Symbol Identifier (编码符号标识符) - 标识该符号在块中的位置
	Sbn               uint32    // Source Block Number (源块编号) - 标识该符号属于哪个源块
	Toi               t.Uint128 // Transport Object Identifier (传输对象标识符) - 标识该包属于哪个传输对象
	FdtID             *uint32   // 文件描述表实例ID（如果是FDT包），可选
	Cenc              lct.Cenc  // 内容编码方式（如gzip/zlib等）
	InbandCenc        bool      // 内容编码信息是否在带内传输
	CloseObject       bool      // 是否关闭对象传输的标志
	SourceBlockLength uint32    // 源块长度（符号数）
	SenderCurrentTime bool      // 是否包含发送方当前时间
}
