/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package meta

import (
	"FluteGo/pkg/sock"
	"fmt"
	"net"
)

// SendFDTXML 发送 FDT XML 到接收端（使用 LCT 头部，TOI=0）
func SendFDTXML(conn *sock.MsSocket, destAddr *net.UDPAddr, fdt *FDTInstance, tsi uint32, fecEncodingID uint8) error {
	if conn == nil {
		return fmt.Errorf("connection is nil")
	}
	if fdt == nil {
		return fmt.Errorf("FDT instance is nil")
	}

	// 序列化 FDT 为 XML
	xmlData, err := fdt.SerializeFDT()
	if err != nil {
		return fmt.Errorf("serialize FDT: %w", err)
	}

	// 构建 LCT 头部（TOI=0 表示 FDT）
	lctHeader := NewLCTHeader(TOIFDT, fecEncodingID, tsi)
	lctBytes, err := lctHeader.Encode()
	if err != nil {
		return fmt.Errorf("encode LCT header: %w", err)
	}

	// 组合 LCT 头部 + FDT XML
	packet := make([]byte, LCTHeaderLength+len(xmlData))
	copy(packet[:LCTHeaderLength], lctBytes)
	copy(packet[LCTHeaderLength:], xmlData)

	// 发送数据包
	_, err = conn.Socket.WriteToUDP(packet, destAddr)
	if err != nil {
		return fmt.Errorf("send FDT XML: %w", err)
	}

	return nil
}

// ReceiveFDTXML 从连接接收 FDT XML（解析 LCT 头部后提取 XML）
func ReceiveFDTXML(conn *sock.MsSocket) (*FDTInstance, uint32, error) {
	if conn == nil {
		return nil, 0, fmt.Errorf("connection is nil")
	}

	// 读取数据
	buf := make([]byte, 65536)
	n, err := conn.Socket.ReadFromUDP(buf)
	if err != nil {
		return nil, 0, fmt.Errorf("read FDT XML: %w", err)
	}

	if n < LCTHeaderLength {
		return nil, 0, fmt.Errorf("packet too short: %d bytes", n)
	}

	// 解析 LCT 头部
	var lctHeader LCTHeader
	if err := lctHeader.Decode(buf[:n]); err != nil {
		return nil, 0, fmt.Errorf("decode LCT header: %w", err)
	}

	// 验证 TOI=0（FDT）
	if lctHeader.TOI != TOIFDT {
		return nil, 0, fmt.Errorf("invalid TOI for FDT: expected 0, got %d", lctHeader.TOI)
	}

	// 提取 FDT XML
	xmlData := buf[LCTHeaderLength:n]

	// 反序列化 XML 为 FDT
	fdt, err := DeserializeFDT(xmlData)
	if err != nil {
		return nil, 0, fmt.Errorf("deserialize FDT: %w", err)
	}

	return fdt, lctHeader.TSI, nil
}
