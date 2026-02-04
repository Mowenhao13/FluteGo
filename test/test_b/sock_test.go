package test

import (
	"FluteGo/constant"
	"FluteGo/pkg/pool"
	"FluteGo/pkg/sock"
	"fmt"
	"net"
	"strings"
	"testing"
)

var destIP = "127.0.0.1"

func sendData(wsck *sock.MsSocket, data []byte) error {
	// 从 globalPool 获取目标 IP 地址
	pool.InitConnPool(destIP, 0)

	sendPool := pool.GetConnPool()
	destIP := sendPool.DestIP
	if destIP == "" {
		return fmt.Errorf("destination IP address not set")
	}

	// 去除空格和换行符
	destIP = strings.TrimSpace(destIP)

	// 创建目标 UDP 地址
	ip := net.ParseIP(destIP)
	if ip == nil {
		return fmt.Errorf("invalid IP address: %s", destIP)
	}

	destAddr := &net.UDPAddr{
		IP:   ip,
		Port: constant.META_PORT,
	}

	_, err := wsck.Socket.WriteToUDP(data, destAddr)
	if err != nil {
		return err
	}

	return nil
}

func TestSendData(t *testing.T) {

	// 创建测试用的 WinSocket
	sock, err := sock.CreateMsSocket(destIP, constant.META_PORT, sock.ModeSend)
	if err != nil {
		t.Fatalf("Failed to create WinSocket: %v", err)
	}
	defer sock.Socket.Close()

	// 测试数据
	testData := []byte("Hello, World!")

	// 发送数据
	bytesSent, err := sock.Socket.WriteToUDP(testData, sock.Addr)
	if err != nil {
		t.Fatalf("SendData failed: %v", err)
	}

	// 验证发送的字节数是否正确
	if bytesSent != len(testData) {
		t.Errorf("Expected %d bytes sent, got %d", len(testData), bytesSent)
	}
}

func TestGetData(t *testing.T) {

	pool.InitConnPool(destIP, 1)

	// 创建测试用的 WinSocket
	sock, err := sock.CreateMsSocket("127.0.0.1", constant.META_PORT, sock.ModeRecv)
	if err != nil {
		t.Fatalf("Failed to create WinSocket: %v", err)
	}
	defer sock.Socket.Close()

	// 初始化接收缓冲区
	buf := make([]byte, 1500)

	// 接收数据
	var bytesRead int
	for {
		bytesRead, err = sock.Socket.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("GetData failed: %v", err)
		}

		// 验证接收的字节数是否大于0
		if bytesRead == 0 {
			t.Fatalf("No data received")
		} else {
			break
		}
	}
	

	// 验证接收的数据
	recvData := buf[:bytesRead]
	t.Logf("Received %d bytes: %s", bytesRead, string(recvData))
}
