//go:build windows
// +build windows

package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage:")
		fmt.Println("  test_udp_windows.exe server  - Run as UDP server (receiver)")
		fmt.Println("  test_udp_windows.exe client <ip> - Run as UDP client (sender)")
		os.Exit(1)
	}

	mode := os.Args[1]

	if mode == "server" {
		runServer()
	} else if mode == "client" {
		if len(os.Args) < 3 {
			fmt.Println("Please specify target IP")
			os.Exit(1)
		}
		runClient(os.Args[2])
	}
}

func runServer() {
	fmt.Println("=== Windows UDP Test Server ===")

	// Test 1: Using standard net.ListenUDP
	fmt.Println("\nTest 1: Using net.ListenUDP (standard library)")
	addr1, err := net.ResolveUDPAddr("udp4", "0.0.0.0:3399")
	if err != nil {
		log.Fatalf("ResolveUDPAddr failed: %v", err)
	}
	conn1, err := net.ListenUDP("udp4", addr1)
	if err != nil {
		log.Fatalf("ListenUDP failed: %v", err)
	}
	fmt.Printf("✓ Listening on %s (net.ListenUDP)\n", conn1.LocalAddr())
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := conn1.ReadFromUDP(buf)
			if err != nil {
				log.Printf("ReadFromUDP error: %v", err)
				return
			}
			fmt.Printf("✓ Received %d bytes from %s: %s\n", n, addr, string(buf[:n]))
		}
	}()

	// Test 2: Using Windows socket directly (like FluteGo does)
	fmt.Println("\nTest 2: Using Windows socket directly (like FluteGo)")
	sock, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		log.Fatalf("Socket failed: %v", err)
	}

	err = windows.SetsockoptInt(sock, windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
	if err != nil {
		log.Fatalf("SO_REUSEADDR failed: %v", err)
	}

	// Test bind with host byte order (original FluteGo bug)
	port1 := 3400
	fmt.Printf("Attempting bind to port %d (host byte order)...\n", port1)
	sockaddr1 := &windows.SockaddrInet4{Port: port1}
	ipAddr := net.ParseIP("0.0.0.0").To4()
	copy(sockaddr1.Addr[:], ipAddr)
	err = windows.Bind(sock, sockaddr1)
	if err != nil {
		fmt.Printf("✗ Bind failed (host byte order): %v\n", err)
	} else {
		fmt.Printf("✓ Bind succeeded (host byte order) - but this is wrong!\n")
		windows.CloseHandle(sock)

		// Create new socket
		sock, err = windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
		if err != nil {
			log.Fatalf("Socket failed: %v", err)
		}
	}

	// Test bind with network byte order (fixed)
	port2 := 3400
	fmt.Printf("Attempting bind to port %d (network byte order)...\n", port2)
	netPort := int(uint16((port2>>8)&0xFF) | uint16((port2&0xFF)<<8))
	sockaddr2 := &windows.SockaddrInet4{Port: netPort}
	copy(sockaddr2.Addr[:], ipAddr)
	err = windows.Bind(sock, sockaddr2)
	if err != nil {
		fmt.Printf("✗ Bind failed (network byte order): %v\n", err)
	} else {
		fmt.Printf("✓ Bind succeeded (network byte order) - this is correct!\n")
	}

	fmt.Println("\n=== Server listening ===")
	fmt.Println("  - Port 3399 (standard library)")
	fmt.Println("  - Port 3400 (Windows socket with network byte order)")
	fmt.Println("\nPress Ctrl+C to exit")

	// Keep server running
	buf := make([]byte, 1500)
	for {
		var addr windows.RawSockaddrAny
		var addrLen int32 = int32(windows.SizeofSockaddrInet4)
		var n uint32
		wsaBuf := windows.WSABuf{Len: uint32(len(buf)), Buf: &buf[0]}
		var flags uint32
		err = windows.WSARecvFrom(sock, &wsaBuf, 1, &n, &flags, &addr, &addrLen, nil, nil)
		if err != nil {
			log.Printf("WSARecvFrom error: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}
		fmt.Printf("✓ Received %d bytes on port 3400: %s\n", n, string(buf[:n]))
	}
}

func runClient(targetIP string) {
	fmt.Println("=== Windows UDP Test Client ===")
	fmt.Printf("Sending to %s:3399 and %s:3400\n", targetIP, targetIP)

	addr1, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:3399", targetIP))
	if err != nil {
		log.Fatalf("ResolveUDPAddr 1 failed: %v", err)
	}

	addr2, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:3400", targetIP))
	if err != nil {
		log.Fatalf("ResolveUDPAddr 2 failed: %v", err)
	}

	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: 0})
	if err != nil {
		log.Fatalf("DialUDP failed: %v", err)
	}

	fmt.Println("✓ Client ready, sending packets...")

	counter := 0
	for {
		msg1 := fmt.Sprintf("Packet #%d to port 3399", counter)
		msg2 := fmt.Sprintf("Packet #%d to port 3400", counter)

		_, err = conn.WriteToUDP([]byte(msg1), addr1)
		if err != nil {
			log.Printf("WriteToUDP 1 failed: %v", err)
		} else {
			fmt.Printf("✓ Sent: %s\n", msg1)
		}

		_, err = conn.WriteToUDP([]byte(msg2), addr2)
		if err != nil {
			log.Printf("WriteToUDP 2 failed: %v", err)
		} else {
			fmt.Printf("✓ Sent: %s\n", msg2)
		}

		counter++
		time.Sleep(1 * time.Second)
	}
}
