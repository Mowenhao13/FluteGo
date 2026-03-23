//go:build windows
// +build windows

package main

import (
	"fmt"
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: test_win_send.exe <target-ip> <port>")
		fmt.Println("Tests Windows UDP sending to Mac/Linux")
		os.Exit(1)
	}

	targetIP := os.Args[1]
	targetPort := 3399 // 默认测试端口
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &targetPort)
	}

	fmt.Printf("=== Windows UDP Send Test ===\n")
	fmt.Printf("Target: %s:%d\n\n", targetIP, targetPort)

	// 创建 Windows socket
	sock, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		fmt.Printf("Socket creation failed: %v\n", err)
		return
	}
	defer windows.CloseHandle(sock)

	// 绑定到本地随机端口
	bindAddr := &windows.SockaddrInet4{
		Port: int(uint16((0>>8)&0xFF) | uint16((0&0xFF)<<8)), // 网络字节序
	}
	ipBind := net.ParseIP("0.0.0.0").To4()
	copy(bindAddr.Addr[:], ipBind)
	if err := windows.Bind(sock, bindAddr); err != nil {
		fmt.Printf("Bind failed: %v\n", err)
		return
	}
	fmt.Println("✓ Socket bound to local port")

	// 准备目标地址
	ip := net.ParseIP(targetIP).To4()
	if ip == nil {
		fmt.Printf("Invalid IP: %s\n", targetIP)
		return
	}

	fmt.Println("\n--- Testing different port byte order methods ---")

	// 测试1: 不转换端口字节序（主机字节序）
	fmt.Println("\nTest 1: Sending with host byte order port...")
	var rawAddr1 windows.RawSockaddrInet4
	rawAddr1.Family = windows.AF_INET
	rawAddr1.Port = uint16(targetPort) // 主机字节序，不转换
	copy(rawAddr1.Addr[:], ip[:4])
	for i := 0; i < 8; i++ {
		rawAddr1.Zero[i] = 0
	}
	var sockAddrAny1 windows.RawSockaddrAny
	unsafePtr1 := (*windows.RawSockaddrInet4)(unsafe.Pointer(&sockAddrAny1))
	*unsafePtr1 = rawAddr1
	addrLen1 := int32(unsafe.Sizeof(rawAddr1))

	msg1 := fmt.Sprintf("TEST1: Host byte order port %d", targetPort)
	buf1 := []byte(msg1)
	wsaBuf1 := windows.WSABuf{Len: uint32(len(buf1)), Buf: &buf1[0]}
	var byteSent1 uint32
	err = windows.WSASendTo(sock, &wsaBuf1, 1, &byteSent1, 0, &sockAddrAny1, addrLen1, nil, nil)
	if err != nil {
		fmt.Printf("  ✗ Failed: %v\n", err)
	} else {
		fmt.Printf("  ✓ Sent %d bytes: %s\n", byteSent1, msg1)
	}

	// 测试2: 转换端口字节序（网络字节序）
	fmt.Println("\nTest 2: Sending with network byte order port...")
	var rawAddr2 windows.RawSockaddrInet4
	rawAddr2.Family = windows.AF_INET
	netPort := uint16((targetPort>>8)&0xFF) | uint16((targetPort&0xFF)<<8)
	rawAddr2.Port = netPort // 网络字节序，手动转换
	copy(rawAddr2.Addr[:], ip[:4])
	for i := 0; i < 8; i++ {
		rawAddr2.Zero[i] = 0
	}
	var sockAddrAny2 windows.RawSockaddrAny
	unsafePtr2 := (*windows.RawSockaddrInet4)(unsafe.Pointer(&sockAddrAny2))
	*unsafePtr2 = rawAddr2
	addrLen2 := int32(unsafe.Sizeof(rawAddr2))

	msg2 := fmt.Sprintf("TEST2: Network byte order port %d (0x%04x)", targetPort, netPort)
	buf2 := []byte(msg2)
	wsaBuf2 := windows.WSABuf{Len: uint32(len(buf2)), Buf: &buf2[0]}
	var byteSent2 uint32
	err = windows.WSASendTo(sock, &wsaBuf2, 1, &byteSent2, 0, &sockAddrAny2, addrLen2, nil, nil)
	if err != nil {
		fmt.Printf("  ✗ Failed: %v\n", err)
	} else {
		fmt.Printf("  ✓ Sent %d bytes: %s\n", byteSent2, msg2)
	}

	// 测试3: 使用标准库发送（参考）
	fmt.Println("\nTest 3: Sending with standard library...")
	stdAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", targetIP, targetPort))
	if err != nil {
		fmt.Printf("  ResolveUDPAddr failed: %v\n", err)
	} else {
		stdConn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: 0})
		if err != nil {
			fmt.Printf("  DialUDP failed: %v\n", err)
		} else {
			defer stdConn.Close()
			msg3 := fmt.Sprintf("TEST3: Standard library port %d", targetPort)
			n, err := stdConn.WriteToUDP([]byte(msg3), stdAddr)
			if err != nil {
				fmt.Printf("  ✗ Failed: %v\n", err)
			} else {
				fmt.Printf("  ✓ Sent %d bytes: %s\n", n, msg3)
			}
		}
	}

	fmt.Println("\n=== Test complete ===")
	fmt.Println("Check the receiver to see which packets arrived!")
}
