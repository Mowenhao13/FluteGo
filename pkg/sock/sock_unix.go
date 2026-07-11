//go:build darwin

package sock

import (
	"fmt"
	"log"
	"net"
	"strings"
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

	// 接收模式需要 SO_REUSEADDR/SO_REUSEPORT，以便多个 socket 绑定到同端口（多播场景）
	if mode == ModeRecv {
		syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1) //nolint:errcheck
		syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEPORT, 1) //nolint:errcheck
	}

	// 根据模式选择绑定地址
	var sockAddr syscall.SockaddrInet4
	if mode == ModeSend {
		// 对于发送者，绑定到本地随机端口
		sockAddr.Port = 0
	} else {
		// 对于接收者，绑定到指定的端口
		// 注意：SockaddrInet4.Port 使用主机字节序，内核会自动转换
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
	// 注意：SockaddrInet4.Port 使用主机字节序，内核会自动转换
	var sockAddr syscall.SockaddrInet4
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
	return syscall.SetsockoptTimeval(s.fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Sec: t.Unix(), Usec: int32(t.UnixNano() % int64(time.Second))})
}

func (s *UnixSocket) SetWriteDeadline(t time.Time) error {
	return syscall.SetsockoptTimeval(s.fd, syscall.SOL_SOCKET, syscall.SO_SNDTIMEO, &syscall.Timeval{Sec: t.Unix(), Usec: int32(t.UnixNano() % int64(time.Second))})
}

func (s *UnixSocket) Shutdown(mode int) error {
	return syscall.Shutdown(s.fd, mode)
}

func (s *UnixSocket) JoinMulticastGroup(mcastIP net.IP, iface *net.Interface) error {
	var joinIP net.IP
	var ifName string

	if iface != nil {
		// 指定了接口：在该接口上加入多播组
		addrs, err := iface.Addrs()
		if err == nil {
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
					joinIP = ipnet.IP.To4()
					ifName = iface.Name
					break
				}
			}
		}
	}

	if joinIP == nil {
		// 未指定接口或找不到：打印所有接口信息用于诊断，然后回退到 INADDR_ANY
		// 让系统内核用默认路由接口（比硬编码 en0/eth0 猜接口更可靠）
		logMulticastInterfaces()
		joinIP = net.IPv4zero
		ifName = "INADDR_ANY (auto)"
		log.Printf("[multicast] WARNING: no specific interface selected, using INADDR_ANY")
		log.Printf("[multicast] HINT: set 'multicastIfaceIP' in config to the host's own LAN IP")
	}

	mreq := syscall.IPMreq{
		Multiaddr: [4]byte{mcastIP[0], mcastIP[1], mcastIP[2], mcastIP[3]},
	}
	copy(mreq.Interface[:], joinIP.To4())

	if err := syscall.SetsockoptIPMreq(s.fd, syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, &mreq); err != nil {
		return fmt.Errorf("join multicast %s on %s failed: %w", mcastIP.String(), ifName, err)
	}
	log.Printf("[multicast] joined group %s on interface %s (%s)", mcastIP.String(), joinIP.String(), ifName)

	// 设置多播出口接口（仅当指定了具体接口时；INADDR_ANY 时让系统用默认路由）
	if joinIP[0] != 0 || joinIP[1] != 0 || joinIP[2] != 0 || joinIP[3] != 0 {
		if err := syscall.SetsockoptInet4Addr(s.fd, syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, mreq.Interface); err != nil {
			log.Printf("[multicast] set IP_MULTICAST_IF to %s failed: %v", joinIP.String(), err)
		}
	}

	// 允许接收多播回环（与 Windows 版本保持一致）
	if err := syscall.SetsockoptInt(s.fd, syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1); err != nil {
		log.Printf("[multicast] set IP_MULTICAST_LOOP failed: %v", err)
	}

	return nil
}

// logMulticastInterfaces 打印所有网络接口信息，用于诊断多播网卡绑定问题。
func logMulticastInterfaces() {
	ifaces, err := net.Interfaces()
	if err != nil {
		log.Printf("[multicast] failed to list interfaces: %v", err)
		return
	}

	log.Printf("[multicast] === available network interfaces ===")
	for i, f := range ifaces {
		flags := ""
		if f.Flags&net.FlagUp != 0 {
			flags += "UP "
		}
		if f.Flags&net.FlagLoopback != 0 {
			flags += "LOOPBACK "
		}
		if f.Flags&net.FlagMulticast != 0 {
			flags += "MULTICAST "
		}

		var addrsStr string
		addrs, err := f.Addrs()
		if err == nil {
			var ipStrs []string
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok {
					ipStrs = append(ipStrs, ipnet.IP.String())
				}
			}
			addrsStr = strings.Join(ipStrs, ", ")
		}

		log.Printf("[multicast]   #%d: name=%q, mac=%x, mtu=%d, flags=[%s], addrs=[%s]",
			i, f.Name, f.HardwareAddr, f.MTU, flags, addrsStr)
	}
	log.Printf("[multicast] === end of interface list ===")
}

func (s *UnixSocket) LeaveMulticastGroup(mcastIP net.IP, iface *net.Interface) error {
	mreq := syscall.IPMreq{
		Multiaddr: [4]byte{mcastIP[0], mcastIP[1], mcastIP[2], mcastIP[3]},
	}
	if iface != nil {
		addrs, err := iface.Addrs()
		if err == nil && len(addrs) > 0 {
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
					copy(mreq.Interface[:], ipnet.IP.To4())
					break
				}
			}
		}
	} else {
		copy(mreq.Interface[:], net.IPv4zero.To4())
	}

	return syscall.SetsockoptIPMreq(s.fd, syscall.IPPROTO_IP, syscall.IP_DROP_MEMBERSHIP, &mreq)
}

func (s *UnixSocket) SetMulticastTTL(ttl int) error {
	return syscall.SetsockoptInt(s.fd, syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, ttl)
}
