package sock

import (
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ModeSend = 0
	ModeRecv = 1
)

// Socket 通用套接字接口
type Socket interface {
	ReadFromUDP(b []byte) (int, error)
	WriteToUDP(b []byte, addr *net.UDPAddr) (int, error)
	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	SetReadBuffer(bytes int) error
	SetWriteBuffer(bytes int) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	Shutdown(mode int) error
	Socket() uintptr
}

type MsSocket struct {
	Addr      *net.UDPAddr
	Socket    Socket
	LastUsed  int64
	LastSent  int64
	IsHealthy bool
	FdtID     uint8
	Mu        sync.RWMutex
	SentData  uint32
	Flags     uint32
}

var createSocket func(addr *net.UDPAddr, mode uint8) (Socket, error)

func CreateMsSocket(ip string, port int, mode uint8) (*MsSocket, error) {
	addr := &net.UDPAddr{
		IP:   net.ParseIP(ip),
		Port: port,
	}

	log.Printf("CreateMsSocket: %s:%d", ip, port)

	socket, err := createSocket(addr, mode)
	if err != nil {
		return nil, err
	}
	return &MsSocket{
		Addr:      addr,
		Socket:    socket,
		IsHealthy: true,
	}, nil
}

func (s *MsSocket) MarkSent() {
	atomic.StoreInt64(&s.LastSent, time.Now().UnixNano())
	atomic.StoreUint32(&s.SentData, 1)
}

func (s *MsSocket) HadSent() bool {
	return atomic.LoadUint32(&s.SentData) == 1
}
