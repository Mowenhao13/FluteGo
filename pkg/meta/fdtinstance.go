/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package meta

import (
	"encoding/xml"
	"fmt"
)

// FDT XML 命名空间
const (
	FDTNamespace  = "urn:IETF:metadata:2005:FLUTE:FDT"
	FluteExtNS    = "urn:flute:ext"
)

// FDTInstance 表示 FDT 实例 (RFC 6726 Section 3.4.2.1)
type FDTInstance struct {
	XMLName xml.Name `xml:"FDT-Instance"`
	XMLNS   string   `xml:"xmlns,attr"`

	// FDT 标识和版本 (FluteGo 扩展,用于增量更新)
	FdtID   uint32 `xml:"Fdt-ID,attr,omitempty"`
	Version uint32 `xml:"Version,attr,omitempty"`

	// RFC 6726 标准属性
	Expires  uint32 `xml:"Expires,attr"`
	Complete bool   `xml:"Complete,attr,omitempty"`

	// 会话级 FEC-OTI (RFC 5052)
	FECOTIFECEncodingID         uint8  `xml:"FEC-OTI-FEC-Encoding-ID,attr,omitempty"`
	FECOTIFECInstanceID         uint16 `xml:"FEC-OTI-FEC-Instance-ID,attr,omitempty"`
	FECOTIMaxSourceBlockLength  uint32 `xml:"FEC-OTI-Maximum-Source-Block-Length,attr,omitempty"`
	FECOTIEncodingSymbolLength  uint16 `xml:"FEC-OTI-Encoding-Symbol-Length,attr,omitempty"`

	// 文件列表
	Files []FDTFile `xml:"File"`

	// FluteGo 扩展: 会话参数
	Session *FDTSession `xml:"flute:session,omitempty"`
}

// FDTFile 表示 FDT 中的文件元素 (RFC 6726 Section 3.4.2.2)
type FDTFile struct {
	ContentLocation string `xml:"Content-Location,attr"`
	TOI             uint32 `xml:"TOI,attr"`
	TransferLength  uint64 `xml:"Transfer-Length,attr"`
	ContentLength   uint64 `xml:"Content-Length,attr,omitempty"`
	ContentType     string `xml:"Content-Type,attr,omitempty"`
	ContentEncoding string `xml:"Content-Encoding,attr,omitempty"`
	ContentMD5      string `xml:"Content-MD5,attr,omitempty"`
	FileETag        string `xml:"File-ETag,attr,omitempty"`

	// Cache-Control (RFC 6726 Section 3.4.2.2)
	// 格式: "no-cache", "max-stale", "max-age=3600", "expires=2024-12-31T23:59:59Z"
	CacheControl string `xml:"Cache-Control,attr,omitempty"`

	// 文件级 FEC-OTI (可选, 覆盖会话级)
	FECOTIFECEncodingID        uint8  `xml:"FEC-OTI-FEC-Encoding-ID,attr,omitempty"`
	FECOTIEncodingSymbolLength uint16 `xml:"FEC-OTI-Encoding-Symbol-Length,attr,omitempty"`
	FECOTIMaxSourceBlockLength uint32 `xml:"FEC-OTI-Maximum-Source-Block-Length,attr,omitempty"`
}

// FDTSession FluteGo 扩展: 会话级参数
type FDTSession struct {
	XMLNS           string  `xml:"xmlns:flute,attr,omitempty"`
	BasePort        int     `xml:"base-port,attr"`
	NumPorts        uint16  `xml:"num-ports,attr"`
	MaxPacketSize   uint16  `xml:"max-packet-size,attr"`
	RedundancyRatio float64 `xml:"redundancy-ratio,attr,omitempty"`
	RateLimitMbps   float64 `xml:"rate-limit-mbps,attr,omitempty"`
}

// SerializeFDT 将 FDTInstance 序列化为 XML 字节流
func (f *FDTInstance) SerializeFDT() ([]byte, error) {
	// 设置命名空间
	if f.XMLNS == "" {
		f.XMLNS = FDTNamespace
	}
	if f.Session != nil && f.Session.XMLNS == "" {
		f.Session.XMLNS = FluteExtNS
	}

	data, err := xml.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal FDT XML: %w", err)
	}

	// 添加 XML 声明头
	return append([]byte(xml.Header), data...), nil
}

// DeserializeFDT 从 XML 字节流反序列化为 FDTInstance
func DeserializeFDT(data []byte) (*FDTInstance, error) {
	var fdt FDTInstance
	if err := xml.Unmarshal(data, &fdt); err != nil {
		return nil, fmt.Errorf("unmarshal FDT XML: %w", err)
	}
	return &fdt, nil
}

// AddFile 添加文件到 FDT
func (f *FDTInstance) AddFile(file FDTFile) {
	f.Files = append(f.Files, file)
}

// RemoveFile 从 FDT 中移除指定 TOI 的文件
func (f *FDTInstance) RemoveFile(toi uint32) {
	for i, file := range f.Files {
		if file.TOI == toi {
			f.Files = append(f.Files[:i], f.Files[i+1:]...)
			return
		}
	}
}

// GetFile 获取指定 TOI 的文件
func (f *FDTInstance) GetFile(toi uint32) *FDTFile {
	for i := range f.Files {
		if f.Files[i].TOI == toi {
			return &f.Files[i]
		}
	}
	return nil
}

// FileCount 返回文件数量
func (f *FDTInstance) FileCount() int {
	return len(f.Files)
}

// NewFDTInstance 创建新的 FDT 实例
func NewFDTInstance(fdtID uint32, version uint32, expires uint32) *FDTInstance {
	return &FDTInstance{
		XMLNS:   FDTNamespace,
		FdtID:   fdtID,
		Version: version,
		Expires: expires,
	}
}
