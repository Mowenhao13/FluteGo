/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package meta

import (
	"FluteGo/constant"
	"FluteGo/pkg/filedesc"
	fd "FluteGo/pkg/filedesc"
	oti "FluteGo/pkg/oti"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// MetaPkt 封装了一个文件传输任务在 FLUTE 会话中的所有元数据。
//
// # 描述
//
// 包含文件描述、OTI、端口与会话信息，用于生成和解析 FLUTE 元数据包。
type MetaPkt struct {
	File             *fd.FileDesc
	Oti              oti.Oti
	BasePort         int
	NumPorts         uint16
	MaxPacketSize    uint16
	TotalFiles       uint16
	CurrentFileIndex uint16
}

// InitMetaPkt 构建一个用于传输的 MetaPkt 实例。
//
// # 参数
//
//   - `file`: 源文件句柄
//   - `oti`: 前向纠错表示
//   - `basePort`, `numPorts`: 用于传输的端口集合
//   - `fdtID`: 当前文件 FDT ID
//
// # 返回值
//
//	初始化好的 `MetaPkt` 以及可能的错误（如文件描述创建失败）。
func InitMetaPkt(file *os.File, oti oti.Oti, basePort int, numPorts uint16, fdtID uint8) (*MetaPkt, error) {
	fd, err := filedesc.GetFileDesc(file, fdtID)
	if err != nil {
		return nil, err
	}

	if oti.MaximumChunkSize == 0 {
		oti.MaximumChunkSize = uint32(constant.DefaultChunkSize)
	}

	return &MetaPkt{
		File:          fd,
		Oti:           oti,
		BasePort:      basePort,
		NumPorts:      numPorts,
		MaxPacketSize: constant.MAX_PACKET_SIZE,
	}, nil
}

// Serialize 将 MetaPkt 序列化为字节流，便于通过网络传输。
//
// # 返回值
//
//	`[]byte`：按照固定字段顺序编码的元数据包。
func (mt *MetaPkt) Serialize() []byte {
	buf := new(bytes.Buffer)

	file := mt.File
	if file == nil {
		file = &fd.FileDesc{}
	}

	// FdtID（使用 4 字节以与 ExtFDT 类型一致）
	binary.Write(buf, binary.BigEndian, uint32(file.FdtID))

	// SendPath 长度 + 内容
	sendPath := []byte(file.SendPath)
	binary.Write(buf, binary.BigEndian, uint16(len(sendPath)))
	buf.Write(sendPath)

	// Name 长度 + 内容
	name := []byte(file.Name)
	binary.Write(buf, binary.BigEndian, uint16(len(name)))
	buf.Write(name)

	// TransferLen
	binary.Write(buf, binary.BigEndian, file.TransferLen)

	// Content-Type 长度 + 内容
	content := []byte(file.ContentType)
	binary.Write(buf, binary.BigEndian, uint16(len(content)))
	buf.Write(content)

	// Md5 长度 + 内容
	md5 := []byte(file.Md5)
	binary.Write(buf, binary.BigEndian, uint16(len(md5)))
	buf.Write(md5)

	// OTI（使用固定宽度）
	binary.Write(buf, binary.BigEndian, mt.Oti.FECEncodingID)
	binary.Write(buf, binary.BigEndian, uint32(mt.Oti.DataShards))
	binary.Write(buf, binary.BigEndian, uint32(mt.Oti.ParityShards))
	binary.Write(buf, binary.BigEndian, uint16(mt.Oti.SymbolSize))
	binary.Write(buf, binary.BigEndian, uint32(mt.Oti.MaximumChunkSize))

	// BasePort / NumPorts
	binary.Write(buf, binary.BigEndian, uint32(mt.BasePort))
	binary.Write(buf, binary.BigEndian, uint16(mt.NumPorts))

	// MaxPacketSize
	binary.Write(buf, binary.BigEndian, uint16(mt.MaxPacketSize))
	// Session information
	binary.Write(buf, binary.BigEndian, mt.TotalFiles)
	binary.Write(buf, binary.BigEndian, mt.CurrentFileIndex)
	return buf.Bytes()
}

// DeserializeMetaPkt 将字节流反序列化为 MetaPkt 结构，要求字段顺序与 Serialize 保持一致。
//
// # 参数
//
//   - `data`: 来自网络的元数据包字节流
//
// # 返回值
//
//	解析出的 `MetaPkt` 实例和可能的错误。
func DeserializeMetaPkt(data []byte) (*MetaPkt, error) {
	buf := bytes.NewReader(data)

	mt := &MetaPkt{
		File: &fd.FileDesc{},
	}

	// FdtID
	var fdtID uint32
	if err := binary.Read(buf, binary.BigEndian, &fdtID); err != nil {
		return nil, err
	}
	mt.File.FdtID = uint8(fdtID)

	// SendPath
	var sendPathLen uint16
	if err := binary.Read(buf, binary.BigEndian, &sendPathLen); err != nil {
		return nil, err
	}
	if sendPathLen > 0 {
		path := make([]byte, sendPathLen)
		if _, err := io.ReadFull(buf, path); err != nil {
			return nil, err
		}
		mt.File.SendPath = string(path)
	}

	// Name
	var nameLen uint16
	if err := binary.Read(buf, binary.BigEndian, &nameLen); err != nil {
		return nil, err
	}
	if nameLen > 0 {
		name := make([]byte, nameLen)
		if _, err := io.ReadFull(buf, name); err != nil {
			return nil, err
		}
		mt.File.Name = string(name)
	}

	// TransferLen
	if err := binary.Read(buf, binary.BigEndian, &mt.File.TransferLen); err != nil {
		return nil, err
	}

	// Content-Type
	var contentTypeLen uint16
	if err := binary.Read(buf, binary.BigEndian, &contentTypeLen); err != nil {
		return nil, err
	}
	if contentTypeLen > 0 {
		content := make([]byte, contentTypeLen)
		if _, err := io.ReadFull(buf, content); err != nil {
			return nil, err
		}
		mt.File.ContentType = string(content)
	}

	// Md5
	var md5Len uint16
	if err := binary.Read(buf, binary.BigEndian, &md5Len); err != nil {
		return nil, err
	}
	if md5Len > 0 {
		md5 := make([]byte, md5Len)
		if _, err := io.ReadFull(buf, md5); err != nil {
			return nil, err
		}
		mt.File.Md5 = string(md5)
	}

	// OTI（必须与 Serialize 顺序一致）
	if err := binary.Read(buf, binary.BigEndian, &mt.Oti.FECEncodingID); err != nil {
		return nil, err
	}

	var dataShards uint32
	if err := binary.Read(buf, binary.BigEndian, &dataShards); err != nil {
		return nil, err
	}
	mt.Oti.DataShards = uint8(dataShards)

	var parityShards uint32
	if err := binary.Read(buf, binary.BigEndian, &parityShards); err != nil {
		return nil, err
	}
	mt.Oti.ParityShards = uint8(parityShards)

	var symbolSize uint16
	if err := binary.Read(buf, binary.BigEndian, &symbolSize); err != nil {
		return nil, err
	}
	mt.Oti.SymbolSize = symbolSize

	var maxChunkSize uint32
	if err := binary.Read(buf, binary.BigEndian, &maxChunkSize); err != nil {
		return nil, err
	}
	mt.Oti.MaximumChunkSize = maxChunkSize

	// BasePort / NumPorts（在 OTI 之后）
	var basePort uint32
	if err := binary.Read(buf, binary.BigEndian, &basePort); err != nil {
		return nil, err
	}
	mt.BasePort = int(basePort)

	var numPorts uint16
	if err := binary.Read(buf, binary.BigEndian, &numPorts); err != nil {
		return nil, err
	}
	mt.NumPorts = numPorts

	// MaxPacketSize
	var maxPacketSize uint16
	if err := binary.Read(buf, binary.BigEndian, &maxPacketSize); err != nil {
		return nil, err
	}
	mt.MaxPacketSize = maxPacketSize
	var totalFiles uint16
	if err := binary.Read(buf, binary.BigEndian, &totalFiles); err != nil {
		return nil, err
	}
	mt.TotalFiles = totalFiles
	var fileIndex uint16
	if err := binary.Read(buf, binary.BigEndian, &fileIndex); err != nil {
		return nil, err
	}
	mt.CurrentFileIndex = fileIndex
	return mt, nil
}

// ShowPktInfo 打印 MetaPkt 详情，主要用于调试。
func (m *MetaPkt) ShowPktInfo() {
	if m == nil {
		fmt.Println("<nil> MetaPkt")
		return
	}

	if m.File == nil {
		fmt.Println("MetaPkt missing file information")
		return
	}

	fmt.Printf("FdtID: %d\n", m.File.FdtID)

	fmt.Printf("File name: %s\n", m.File.Name)
	fmt.Printf("File transfer len: %d\n", m.File.TransferLen)
	fmt.Printf("File content type: %s\n", m.File.ContentType)
	fmt.Printf("File md5sum: %s\n", m.File.Md5)
	fmt.Printf("File send path: %s\n", m.File.SendPath)

	fmt.Printf("Oti id: %d\n", m.Oti.FECEncodingID)
	fmt.Printf("BasePort: %d\n", m.BasePort)
	fmt.Printf("NumPort: %d\n", m.NumPorts)
	fmt.Printf("MaxPacketSize: %d\n", m.MaxPacketSize)
	if m.TotalFiles > 0 {
		fmt.Printf("Session files: %d\n", m.TotalFiles)
	}
	if m.CurrentFileIndex > 0 {
		fmt.Printf("Current file index: %d\n", m.CurrentFileIndex)
	}
}
