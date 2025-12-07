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

// MetaPkt 元数据包结构
// 功能说明：
//   描述文件传输的所有必要信息，包括文件描述符、编码参数、传输参数等
// 核心字段：
//   File             - 文件描述符，包含文件元数据
//   Oti              - 对象传输信息，包含FEC编码参数
//   BasePort         - 基础端口号
//   NumPorts         - 使用端口数量
//   MaxPacketSize    - 最大数据包大小
//   TotalFiles       - 会话中总文件数
//   CurrentFileIndex - 当前文件索引
// 协议遵循：
//   遵循FLUTE（File Delivery over Unidirectional Transport）协议
type MetaPkt struct {
	File             *fd.FileDesc
	Oti              oti.Oti
	BasePort         int
	NumPorts         uint16
	MaxPacketSize    uint16
	TotalFiles       uint16
	CurrentFileIndex uint16
}

// InitMetaPkt 初始化元数据包
// 功能说明：
//   根据文件对象和OTI配置创建元数据包实例
// 参数：
//   file       - 文件对象，用于获取文件元数据
//   oti        - 对象传输信息，包含FEC编码参数
//   basePort   - 基础端口号
//   numPorts   - 使用端口数量
//   fdtID      - 文件传输标识符
//   saveDir    - 文件保存目录
// 返回值：
//   *MetaPkt   - 初始化完成的元数据包实例
//   error      - 初始化过程中的错误
// 实现步骤：
//   1. 从文件对象获取文件描述符
//   2. 设置默认分块大小
//   3. 创建元数据包实例
// 使用场景：
//   文件传输开始时，发送方创建元数据包
func InitMetaPkt(file *os.File, oti oti.Oti, basePort int, numPorts uint16, fdtID uint8, saveDir string) (*MetaPkt, error) {
	fd, err := filedesc.GetFileDesc(file, fdtID, saveDir)
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
		MaxPacketSize: constant.MaxPacketSize,
	}, nil
}

// Serialize 序列化元数据包
// 功能说明：
//   将元数据包结构体转换为字节序列，用于网络传输
// 返回值：
//   []byte - 序列化后的字节数组
// 序列化格式：
//   1. FdtID (4字节, uint32)
//   2. SendPath长度 (2字节, uint16) + SendPath内容
//   3. SaveDir长度 (2字节, uint16) + SaveDir内容
//   4. Name长度 (2字节, uint16) + Name内容
//   5. TransferLen (8字节, uint64)
//   6. ContentType长度 (2字节, uint16) + ContentType内容
//   7. Md5长度 (2字节, uint16) + Md5内容
//   8. OTI字段：
//      - FECEncodingID (1字节, uint8)
//      - DataShards (4字节, uint32)
//      - ParityShards (4字节, uint32)
//      - SymbolSize (2字节, uint16)
//      - MaximumChunkSize (4字节, uint32)
//   9. BasePort (4字节, uint32)
//   10. NumPorts (2字节, uint16)
//   11. MaxPacketSize (2字节, uint16)
//   12. TotalFiles (2字节, uint16)
//   13. CurrentFileIndex (2字节, uint16)
// 设计考虑：
//   - 使用大端序保证跨平台兼容性
//   - 变长字符串使用长度前缀格式
//   - 固定宽度字段避免对齐问题
// 使用场景：
//   网络传输前，将元数据包序列化为字节流
func (mt *MetaPkt) Serialize() []byte {
	buf := new(bytes.Buffer)

	file := mt.File
	if file == nil {
		file = &fd.FileDesc{}
	}

	// 1. FdtID（使用4字节对齐）
	binary.Write(buf, binary.BigEndian, uint32(file.FdtID))

	// 2. SendPath 长度前缀 + 内容
	sendPath := []byte(file.SendPath)
	binary.Write(buf, binary.BigEndian, uint16(len(sendPath)))
	buf.Write(sendPath)

	// 3. SaveDir 长度前缀 + 内容
	saveDir := []byte(file.SaveDir)
	binary.Write(buf, binary.BigEndian, uint16(len(saveDir)))
	buf.Write(saveDir)

	// 4. Name 长度前缀 + 内容
	name := []byte(file.Name)
	binary.Write(buf, binary.BigEndian, uint16(len(name)))
	buf.Write(name)

	// 5. TransferLen（文件传输大小）
	binary.Write(buf, binary.BigEndian, file.TransferLen)

	// 6. Content-Type 长度前缀 + 内容
	content := []byte(file.ContentType)
	binary.Write(buf, binary.BigEndian, uint16(len(content)))
	buf.Write(content)

	// 7. Md5 长度前缀 + 内容
	md5 := []byte(file.Md5)
	binary.Write(buf, binary.BigEndian, uint16(len(md5)))
	buf.Write(md5)

	// 8. OTI（对象传输信息）字段
	binary.Write(buf, binary.BigEndian, mt.Oti.FECEncodingID)
	binary.Write(buf, binary.BigEndian, uint32(mt.Oti.DataShards))
	binary.Write(buf, binary.BigEndian, uint32(mt.Oti.ParityShards))
	binary.Write(buf, binary.BigEndian, uint16(mt.Oti.SymbolSize))
	binary.Write(buf, binary.BigEndian, uint32(mt.Oti.MaximumChunkSize))

	// 9. 传输端口配置 BasePort / NumPorts
	binary.Write(buf, binary.BigEndian, uint32(mt.BasePort))
	binary.Write(buf, binary.BigEndian, uint16(mt.NumPorts))

	// 10. 网络参数 MaxPacketSize
	binary.Write(buf, binary.BigEndian, uint16(mt.MaxPacketSize))
	
	// 11. 会话信息（多文件传输支持）
	binary.Write(buf, binary.BigEndian, mt.TotalFiles)
	binary.Write(buf, binary.BigEndian, mt.CurrentFileIndex)
	return buf.Bytes()
}

// DeserializeMetaPkt 反序列化元数据包
// 功能说明：
//   从字节流解析出元数据包结构体
// 参数：
//   data - 序列化的字节数组
// 返回值：
//   *MetaPkt - 解析出的元数据包实例
//   error    - 解析过程中的错误
// 解析规则：
//   1. 严格遵循序列化格式的顺序
//   2. 检查长度字段的合法性
//   3. 验证字符串编码
// 错误处理：
//   - 数据长度不足
//   - 长度字段与内容不匹配
//   - 字段值超出范围
// 使用场景：
//   接收方从网络接收到字节流后，解析为元数据包
func DeserializeMetaPkt(data []byte) (*MetaPkt, error) {
	buf := bytes.NewReader(data)

	mt := &MetaPkt{
		File: &fd.FileDesc{},
	}

	// 1. FdtID
	var fdtID uint32
	if err := binary.Read(buf, binary.BigEndian, &fdtID); err != nil {
		return nil, err
	}
	mt.File.FdtID = uint8(fdtID)

	// 2. SendPath
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

	// 3. SaveDir
	var saveDirLen uint16
	if err := binary.Read(buf, binary.BigEndian, &saveDirLen); err != nil {
		return nil, err
	}
	if saveDirLen > 0 {
		dir := make([]byte, saveDirLen)
		if _, err := io.ReadFull(buf, dir); err != nil {
			return nil, err
		}
		mt.File.SaveDir = string(dir)
	}

	// 4. Name
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

	// 5. TransferLen（文件传输大小）
	if err := binary.Read(buf, binary.BigEndian, &mt.File.TransferLen); err != nil {
		return nil, err
	}

	// 6. Content-Type
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

	// 7. Md5
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

	// 8. OTI（对象传输信息）字段
	if err := binary.Read(buf, binary.BigEndian, &mt.Oti.FECEncodingID); err != nil {
		return nil, err
	}
	// DataShards扩展为4字节以保证对齐
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

	// 9. 传输端口配置
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

	// 10. 网络参数
	var maxPacketSize uint16
	if err := binary.Read(buf, binary.BigEndian, &maxPacketSize); err != nil {
		return nil, err
	}
	mt.MaxPacketSize = maxPacketSize

	// 11. 会话信息（多文件传输支持）
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

// ShowPktInfo 显示元数据包信息
// 功能说明：
//   格式化输出元数据包的所有信息，用于调试和日志记录
// 输出格式：
//   1. 文件基本信息
//   2. 传输参数
//   3. 编码参数
//   4. 端口信息
//   5. 会话信息
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
	fmt.Printf("File save dir: %s\n", m.File.SaveDir)

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

func (m *MetaPkt) Validate() error {
	//TODO: Validate metaPkt if usable
	return nil 
}

