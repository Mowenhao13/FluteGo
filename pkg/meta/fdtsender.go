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

// SendFDTXML 发送 FDT XML 到接收端
func SendFDTXML(conn *sock.MsSocket, destAddr *net.UDPAddr, fdt *FDTInstance) error {
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

	// 发送 XML 数据
	_, err = conn.Socket.WriteToUDP(xmlData, destAddr)
	if err != nil {
		return fmt.Errorf("send FDT XML: %w", err)
	}

	return nil
}

// ReceiveFDTXML 从连接接收 FDT XML
func ReceiveFDTXML(conn *sock.MsSocket) (*FDTInstance, error) {
	if conn == nil {
		return nil, fmt.Errorf("connection is nil")
	}

	// 读取数据
	buf := make([]byte, 65536)
	n, err := conn.Socket.ReadFromUDP(buf)
	if err != nil {
		return nil, fmt.Errorf("read FDT XML: %w", err)
	}

	// 反序列化 XML 为 FDT
	fdt, err := DeserializeFDT(buf[:n])
	if err != nil {
		return nil, fmt.Errorf("deserialize FDT: %w", err)
	}

	return fdt, nil
}
