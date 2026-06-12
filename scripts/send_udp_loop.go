package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	targetIP   = "192.168.0.12"
	targetPort = 3400
	packetLen  = 64
)

func main() {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", targetIP, targetPort))
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve addr: %v\n", err)
		os.Exit(1)
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial udp: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	payload := make([]byte, packetLen)
	for i := range payload {
		payload[i] = byte(i)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	var sent int64
	start := time.Now()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	fmt.Printf("Sending %d-byte UDP packets to %s:%d ... (Ctrl+C to stop)\n", packetLen, targetIP, targetPort)

loop:
	for {
		select {
		case <-ticker.C:
			if _, err := conn.Write(payload); err != nil {
				fmt.Fprintf(os.Stderr, "\nwrite error: %v\n", err)
				break loop
			}
			sent++
			if sent%1000 == 0 {
				elapsed := time.Since(start)
				mbps := float64(sent*packetLen*8) / elapsed.Seconds() / 1e6
				fmt.Printf("\rSent %d packets (%.1f Mbps)    ", sent, mbps)
			}
		case <-sig:
			break loop
		}
	}

	elapsed := time.Since(start)
	mbps := float64(sent*packetLen*8) / elapsed.Seconds() / 1e6
	fmt.Printf("\nDone — %d packets sent in %s (%.1f Mbps)\n", sent, elapsed.Round(time.Millisecond), mbps)
}
