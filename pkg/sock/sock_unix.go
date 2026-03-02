//go:build !windows

package sock

import (
	"net"
	"syscall"
	"time"
)

type UnixSocket struct {
	fd   int
	addr *net.UDPAddr
}

func init() {
	createSocket = newUnixSocket
}

func newUnixSocket(addr *net.UDPAddr, mode uint8) (Socket, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_UDP)
	if err != nil {
		return nil, err
	}

	// 根据模式选择绑定地址
	var sockAddr syscall.SockaddrInet4
	if mode == ModeSend {
		// 对于发送者，绑定到本地随机端口
		sockAddr.Port = 0
	} else {
		// 对于接收者，绑定到指定的端口
		// syscall.SockaddrInet4.Port 使用主机字节序，syscall.Bind 内部会自动转换
		sockAddr.Port = addr.Port
		copy(sockAddr.Addr[:], addr.IP.To4())
	}

	if err := syscall.Bind(fd, &sockAddr); err != nil {
		return nil, err
	}

	return &UnixSocket{
		fd:   fd,
		addr: addr, // 保存目标地址，用于发送数据
	}, nil
}

func (s *UnixSocket) Socket() uintptr {
	return uintptr(s.fd)
}

func (s *UnixSocket) WriteToUDP(buf []byte, addr *net.UDPAddr) (int, error) {
	// 如果没有提供目标地址，使用创建时的地址
	if addr == nil {
		addr = s.addr
	}

	// 构建 sockaddr_in 结构
	var sockAddr syscall.SockaddrInet4
	// syscall.SockaddrInet4.Port 使用主机字节序，syscall.Sendto 内部会自动转换
	sockAddr.Port = addr.Port
	copy(sockAddr.Addr[:], addr.IP.To4())

	err := syscall.Sendto(s.fd, buf, 0, &sockAddr)
	if err != nil {
		return 0, err
	}
	return len(buf), nil
}

// 辅助函数：将端口转换为网络字节序
func htons(port uint16) int {
	return int((port<<8)&0xff00 | (port>>8)&0x00ff)
}

func (s *UnixSocket) ReadFromUDP(buf []byte) (int, error) {
	n, _, err := syscall.Recvfrom(s.fd, buf, 0)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *UnixSocket) Close() error {
	return syscall.Close(s.fd)
}

func (s *UnixSocket) LocalAddr() net.Addr {
	return s.addr
}

func (s *UnixSocket) RemoteAddr() net.Addr {
	return s.addr
}

func (s *UnixSocket) SetReadBuffer(bytes int) error {
	return syscall.SetsockoptInt(s.fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, bytes)
}

func (s *UnixSocket) SetWriteBuffer(bytes int) error {
	return syscall.SetsockoptInt(s.fd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, bytes)
}

func (s *UnixSocket) SetReadDeadline(t time.Time) error {
	d := time.Until(t)
	if d < 0 {
		d = 0
	}
	tv := syscall.NsecToTimeval(d.Nanoseconds())
	return syscall.SetsockoptTimeval(s.fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
}

func (s *UnixSocket) SetWriteDeadline(t time.Time) error {
	d := time.Until(t)
	if d < 0 {
		d = 0
	}
	tv := syscall.NsecToTimeval(d.Nanoseconds())
	return syscall.SetsockoptTimeval(s.fd, syscall.SOL_SOCKET, syscall.SO_SNDTIMEO, &tv)
}

func (s *UnixSocket) Shutdown(mode int) error {
	return syscall.Shutdown(s.fd, mode)
}
