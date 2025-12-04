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

type MetaPkt struct {
	File             *fd.FileDesc
	Oti              oti.Oti
	BasePort         int
	NumPorts         uint16
	MaxPacketSize    uint16
	TotalFiles       uint16
	CurrentFileIndex uint16
}

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

	// SaveDir 长度 + 内容
	saveDir := []byte(file.SaveDir)
	binary.Write(buf, binary.BigEndian, uint16(len(saveDir)))
	buf.Write(saveDir)

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

	// SaveDir
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
