/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package oti

import (
	"FluteGo/constant"
)

// Oti 对象传输信息 (Object Transmission Information)
//
// # 描述
//
// Oti 结构体包含了 FLUTE 协议中用于描述文件传输的前向纠错 (FEC) 编码参数。
// 它是发送端和接收端协商编码方案的关键信息。
//
// # 字段
//
//   - `FECEncodingID`: `uint8`
//     前向纠错编码算法的标识符。例如：0 表示无编码，1 表示 RaptorQ，2 表示 Reed-Solomon。
//
//   - `FECInstanceID`: `uint16`
//     编码算法的具体实例标识符，用于区分同一算法的不同配置或版本。
//
//   - `MaximumChunkSize`: `uint32`
//     每个 source block 包含的最大 symbol 数量。源文件会被分割成多个块进行编码传输。
//
//   - `SymbolSize`: `uint16`
//     符号大小（字节）。编码符号是 FEC 方案生成的最小数据单元。
//     它是 ALC/LCT 数据包的有效载荷大小。
//
//   - `DataShards`: `uint8`
//     数据分片数量。仅用于 Reed-Solomon 编码，表示原始数据被切分的份数。
//
//   - `ParityShards`: `uint8`
//     校验分片数量。仅用于 Reed-Solomon 编码，表示生成的冗余校验份数。
//
// # 参考
//
//	RFC 5052 (RaptorQ), RFC 5510 (Reed-Solomon)
type Oti struct {
	FECEncodingID    uint8
	FECInstanceID    uint16
	MaximumChunkSize uint32
	SymbolSize       uint16
	DataShards       uint8
	ParityShards     uint8
}

// NewNoCode 创建并返回一个不使用 FEC 编码（No-Code）的 `Oti` 实例。
//
// # 参数
//
//   - `SymbolSize`: `uint16`
//     符号大小（字节）。在此模式下，通常对应于直接传输的数据包大小。
//
// # 返回值
//
//	返回一个配置为无编码模式的 `Oti` 结构体实例。
//
// # 默认值
//
//   - `FECEncodingID`: 0 (NoCode)
//   - `FECInstanceID`: 0
//   - `MaximumChunkSize`: `constant.MaxNoCodeChunkSize` (32 个 symbol)
//
// # 示例
//
// ```go
// import "FluteGo/pkg/oti"
//
// // 创建一个符号大小为 1400 字节的无编码 OTI
// noCodeOti := oti.NewNoCode(1400)
// ```
func NewNoCode(SymbolSize uint16) Oti {
	return Oti{
		FECEncodingID:    0,
		FECInstanceID:    0,
		SymbolSize:       SymbolSize,
		MaximumChunkSize: uint32(constant.MaxNoCodeChunkSize),
	}
}

// NewRaptorQ 创建并返回一个使用 RaptorQ FEC 方案的 `Oti` 实例。
//
// # 参数
//
//   - `SymbolSize`: `uint16`
//     符号大小（字节）。
//
// # 返回值
//
//	返回一个配置为 RaptorQ 编码模式的 `Oti` 结构体实例。
//
// # 默认值
//
//   - `FECEncodingID`: 1 (RaptorQ)
//   - `FECInstanceID`: 1
//   - `MaximumChunkSize`: `constant.MaxRaptorQChunkSize` (32 个 symbol)
//
// # 示例
//
// ```go
// import "FluteGo/pkg/oti"
//
// // 创建一个符号大小为 1400 字节的 RaptorQ OTI
// raptorQOti := oti.NewRaptorQ(1400)
// ```
func NewRaptorQ(SymbolSize uint16) Oti {
	return Oti{
		FECEncodingID:    1,
		FECInstanceID:    1,
		SymbolSize:       SymbolSize,
		MaximumChunkSize: uint32(constant.MaxRaptorQChunkSize),
	}
}

// NewReedSolomon 创建并返回一个使用 Reed-Solomon FEC 方案的 `Oti` 实例。
//
// # 参数
//
//   - `dataShards`: `uint8`
//     数据分片数量。
//
//   - `parityShards`: `uint8`
//     校验分片数量。
//
// # 返回值
//
//	返回一个配置为 Reed-Solomon 编码模式的 `Oti` 结构体实例。
//
// # 默认值
//
//   - `FECEncodingID`: 2 (Reed-Solomon)
//   - `FECInstanceID`: 2
//   - `SymbolSize`: `constant.MaxPacketSize`
//
// # 示例
//
// ```go
// import "FluteGo/pkg/oti"
//
// // 创建一个 10 个数据分片，4 个校验分片的 Reed-Solomon OTI
// rsOti := oti.NewReedSolomon(10, 4)
// ```
func NewReedSolomon(dataShards, parityShards uint8) Oti {
	return Oti{
		FECEncodingID: 2,
		FECInstanceID: 2,
		DataShards:    dataShards,
		ParityShards:  parityShards,
		SymbolSize:    uint16(constant.MAX_PACKET_SIZE),
	}
}
