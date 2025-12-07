/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package oti

import (
	"FluteGo/constant"
	"os"
)

// OTI 对象传输信息结构
// 功能说明：
//   描述文件传输的前向纠错（FEC）编码参数，是FLUTE协议中的关键参数
// 核心字段：
//   FECEncodingID    - 前向纠错编码算法标识
//   FECInstanceID    - 编码算法实例标识
//   MaximumChunkSize  - 最大分块大小（字节）
//   SymbolSize        - 符号大小（字节）
//   DataShards        - 数据分片数量（仅用于Reed-Solomon）
//   ParityShards      - 校验分片数量（仅用于Reed-Solomon）
// 使用场景：
//   1. 发送端构造OTI信息，告知接收端如何解码
//   2. 接收端解析OTI信息，选择合适的解码器
// 协议遵循：
//   遵循RFC 5052（RaptorQ FEC）和RFC 5510（Reed-Solomon FEC）标准
type Oti struct {
	FECEncodingID    uint8
	FECInstanceID    uint16
	MaximumChunkSize uint32
	SymbolSize       uint16
	DataShards       uint8
	ParityShards     uint8
}

// NewNoCode 创建无FEC编码的OTI
// 功能说明：
//   创建无前向纠错编码的OTI配置
// 适用场景：
//   1. 网络质量好，不需要纠错
//   2. 实时性要求高，延迟敏感
//   3. 小文件传输
// 参数：
//   symbolSize - 符号大小（字节）
// 返回值：
//   Oti - 配置好的OTI结构
// 默认值：
//   FECEncodingID: 0 (NoCode)
//   FECInstanceID: 0
//   MaximumChunkSize: 32 * 1024
func NewNoCode(SymbolSize uint16) Oti {
	maxChunkSize := constant.MaxNoCodeChunkSize
	pageSize := os.Getpagesize()
	var alignedSize uint32 
	if maxChunkSize % pageSize != 0 {
		alignedSize = uint32(((maxChunkSize + pageSize - 1) / pageSize - 1) / pageSize)
	}
	return Oti{
		FECEncodingID:    0,
		FECInstanceID:    0,
		SymbolSize:       SymbolSize,
		MaximumChunkSize: alignedSize,
	}
}

// NewRaptorQ 创建RaptorQ编码的OTI
// 功能说明：
//   创建RaptorQ前向纠错编码的OTI配置
// 特性：
//   - 支持大规模数据修复
//   - 编码效率高
//   - 支持任意丢包率修复
// 适用场景：
//   1. 高丢包率网络环境
//   2. 大文件传输
//   3. 广播/多播场景
// 参数：
//   symbolSize - 符号大小（字节）
// 返回值：
//   Oti - 配置好的OTI结构
// 默认值：
//   FECEncodingID: 1 (RaptorQ)
//   FECInstanceID: 1
//   MaximumChunkSize: 32 * 1024
func NewRaptorQ(SymbolSize uint16) Oti {
	maxChunkSize := constant.MaxRaptorQChunkSize
	pageSize := os.Getpagesize()
	var alignedSize uint32 
	if maxChunkSize % pageSize != 0 {
		alignedSize = uint32(((maxChunkSize + pageSize - 1) / pageSize - 1) / pageSize)
	}

	return Oti{
		FECEncodingID:    1,
		FECInstanceID:    1,
		SymbolSize:       SymbolSize,
		MaximumChunkSize: alignedSize,
	}
}

// NewReedSolomon 创建Reed-Solomon编码的OTI
// 功能说明：
//   创建Reed-Solomon前向纠错编码的OTI配置
// 特性：
//   - 支持有限数量的数据修复
//   - 计算复杂度相对较低
//   - 需要预先知道数据/校验分片比例
// 适用场景：
//   1. 已知网络丢包率
//   2. 中小规模文件传输
//   3. 计算资源有限的环境
// 参数：
//   dataShards   - 数据分片数量
//   parityShards - 校验分片数量
// 返回值：
//   Oti - 配置好的OTI结构
// 默认值：
//   FECEncodingID: 2 (Reed-Solomon)
//   FECInstanceID: 2
//   SymbolSize: 常量 MaxPacketSize
func NewReedSolomon(dataShards, parityShards uint8) Oti {
	return Oti{
		FECEncodingID: 2,
		FECInstanceID: 2,
		DataShards:    dataShards,
		ParityShards:  parityShards,
		SymbolSize: uint16(constant.MaxPacketSize),
	}
}