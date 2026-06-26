//go:build windows
// +build windows

package sock

import (
	"fmt"
	"log"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 定义Windows socket选项常量
const (
	// SO_SNDTIMEO 发送超时选项
	SO_SNDTIMEO = 0x1005
	// SO_RCVTIMEO 接收超时选项
	SO_RCVTIMEO = 0x1006
)

type WinSocket struct {
	handle windows.Handle
	addr   *net.UDPAddr
}

func init() {
	createSocket = newWinSocket
}

func newWinSocket(addr *net.UDPAddr, mode uint8) (Socket, error) {
	// 根据模式选择绑定地址
	var ip string
	var port int
	if mode == ModeSend {
		// 对于发送者，绑定到本地随机端口
		ip = "0.0.0.0"
		port = 0
	} else {
		// 对于接收者，绑定到指定的地址和端口
		ip = addr.IP.String()
		port = addr.Port
	}

	handle, err := createWinSocket(ip, port)
	if err != nil {
		return nil, err
	}

	return &WinSocket{
		handle: handle,
		addr:   addr, // 保存目标地址，用于发送数据
	}, nil
}

func createWinSocket(ip string, port int) (windows.Handle, error) {
	sock, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		return 0, fmt.Errorf("Create UDP socket failed: %v", err)
	}

	if err := windows.SetsockoptInt(sock, windows.SOL_SOCKET, windows.SO_REUSEADDR, 1); err != nil {
		windows.CloseHandle(sock)
		return 0, fmt.Errorf("Set SO_REUSEADDR failed: %v", err)
	}

	// 注意：不在 UDP socket 上设置 TCP_NODELAY —— 它是 TCP 选项，对 UDP 无效且可能引发问题

	sockaddr := &windows.SockaddrInet4{
		Port: port, // SockaddrInet4.Port 使用主机字节序，内核会自动转换
	}
	ipAddr := net.ParseIP(ip).To4()
	copy(sockaddr.Addr[:], ipAddr)
	if err := windows.Bind(sock, sockaddr); err != nil {
		windows.CloseHandle(sock)
		return 0, fmt.Errorf("Bind socket failed: %v", err)
	}

	return sock, nil
}

func (s *WinSocket) Socket() uintptr {
	return uintptr(s.handle)
}

func (s *WinSocket) WriteToUDP(buf []byte, addr *net.UDPAddr) (int, error) {
	sockAddr, sockAddrLen, err := udpAddrToRawSockaddr(addr)
	if err != nil {
		return 0, err
	}

	var byteSent uint32
	wsaBuf := windows.WSABuf{
		Len: uint32(len(buf)),
		Buf: &buf[0],
	}

	err = windows.WSASendTo(s.handle, &wsaBuf, 1, &byteSent, 0, sockAddr, sockAddrLen, nil, nil)
	if err != nil {
		return 0, err
	}

	return int(byteSent), nil
}

func (s *WinSocket) ReadFromUDP(b []byte) (int, error) {
	var byteReceived uint32
	wsaBuf := windows.WSABuf{
		Len: uint32(len(b)),
		Buf: &b[0],
	}

	var flags uint32
	fromAddr := &windows.RawSockaddrAny{}
	fromLen := int32(unsafe.Sizeof(*fromAddr))

	err := windows.WSARecvFrom(s.handle, &wsaBuf, 1, &byteReceived, &flags, fromAddr, &fromLen, nil, nil)
	if err != nil {
		// 转换为 net.Error 类型，以便调用方可以正确处理超时
		if err == windows.WSAETIMEDOUT {
			return 0, &net.OpError{
				Op:   "read",
				Net:  "udp",
				Addr: s.addr,
				Err:  timeoutError{},
			}
		}
		return 0, &net.OpError{
			Op:   "read",
			Net:  "udp",
			Addr: s.addr,
			Err:  err,
		}
	}

	return int(byteReceived), nil
}

// timeoutError 实现 net.Error 接口
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func (s *WinSocket) Close() error {
	return windows.CloseHandle(s.handle)
}

func (s *WinSocket) SetReadBuffer(size int) error {
	return windows.SetsockoptInt(s.handle, windows.SOL_SOCKET, windows.SO_RCVBUF, size)
}

func (s *WinSocket) SetWriteBuffer(size int) error {
	return windows.SetsockoptInt(s.handle, windows.SOL_SOCKET, windows.SO_SNDBUF, size)
}

func (s *WinSocket) SetReadDeadline(t time.Time) error {
	// Windows 上暂时禁用超时设置，避免 SetWaitableTimer 错误
	// return windows.SetsockoptTimeval(s.handle, windows.SOL_SOCKET, SO_RCVTIMEO, &windows.Timeval{Sec: int32(t.Unix()), Usec: int32(t.UnixNano() % int64(time.Second))})
	return nil
}

func (s *WinSocket) SetWriteDeadline(t time.Time) error {
	// Windows 上暂时禁用超时设置，避免 SetWaitableTimer 错误
	// return windows.SetsockoptTimeval(s.handle, windows.SOL_SOCKET, SO_SNDTIMEO, &windows.Timeval{Sec: int32(t.Unix()), Usec: int32(t.UnixNano() % int64(time.Second))})
	return nil
}

func (s *WinSocket) LocalAddr() net.Addr {
	return s.addr
}

func (s *WinSocket) RemoteAddr() net.Addr {
	return s.addr
}

func udpAddrToRawSockaddr(addr *net.UDPAddr) (*windows.RawSockaddrAny, int32, error) {
	if addr == nil {
		return nil, 0, fmt.Errorf("addr is nil")
	}
	ip := addr.IP
	if ip == nil {
		return nil, 0, fmt.Errorf("IP address is nil")
	}

	// 处理IPv4
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4ToWindowsRawSockaddr(ipv4, addr.Port)
	}

	return nil, 0, fmt.Errorf("invalid IP address format")
}

// IPv4地址转换
func ipv4ToWindowsRawSockaddr(ipv4 net.IP, port int) (*windows.RawSockaddrAny, int32, error) {
	var rawAddr windows.RawSockaddrInet4

	// 设置地址族
	rawAddr.Family = windows.AF_INET

	// Windows 的 RawSockaddrInet4 结构体与 SockaddrInet4 不同
	// WSASendTo 函数会自动处理端口字节序，或者该结构体使用主机字节序
	rawAddr.Port = uint16((port>>8)&0xFF) | uint16((port&0xFF)<<8)

	// 复制IP地址
	copy(rawAddr.Addr[:], ipv4[:4])

	// Zero字段清零（Windows特有的8字节填充）
	for i := 0; i < 8; i++ {
		rawAddr.Zero[i] = 0
	}

	// 转换为RawSockaddrAny
	var sockAddrAny windows.RawSockaddrAny
	unsafePtr := (*windows.RawSockaddrInet4)(unsafe.Pointer(&sockAddrAny))
	*unsafePtr = rawAddr

	addrLen := int32(unsafe.Sizeof(rawAddr))

	return &sockAddrAny, addrLen, nil
}

func (s *WinSocket) Shutdown(mode int) error {
	return windows.Shutdown(s.handle, mode)
}

func (s *WinSocket) JoinMulticastGroup(mcastIP net.IP, iface *net.Interface) error {
	// 收集要加入的接口 IPv4 地址列表
	var targets []net.IP

	if iface != nil {
		// 指定了接口：只加入该接口
		addrs, err := iface.Addrs()
		if err == nil {
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
					targets = append(targets, ipnet.IP.To4())
					break
				}
			}
		}
		if len(targets) == 0 {
			targets = append(targets, net.IPv4zero)
		}
	} else {
		// 未指定接口：在所有非回环、已启用的 IPv4 接口上都加入多播
		targets = listMulticastInterfaces()
	}

	// 在每个目标接口上加入多播组
	var firstErr error
	for _, ip := range targets {
		mreq := windows.IPMreq{}
		copy(mreq.Multiaddr[:], mcastIP.To4())
		copy(mreq.Interface[:], ip.To4())
		if err := windows.SetsockoptIPMreq(s.handle, windows.IPPROTO_IP, windows.IP_ADD_MEMBERSHIP, &mreq); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[multicast] join %s on interface %s failed: %v", mcastIP.String(), ip.String(), err)
		}
	}
	return firstErr
}

// listMulticastInterfaces 返回所有适合加入多播的接口 IPv4 地址。
// 筛选条件：非回环、已启用、有 IPv4 地址。
// 兜底返回 0.0.0.0。
func listMulticastInterfaces() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return []net.IP{net.IPv4zero}
	}

	var result []net.IP
	for _, f := range ifaces {
		if f.Flags&net.FlagLoopback != 0 || f.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := f.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				result = append(result, ipnet.IP.To4())
				break
			}
		}
	}

	if len(result) == 0 {
		result = append(result, net.IPv4zero)
	}
	return result
}

func (s *WinSocket) LeaveMulticastGroup(mcastIP net.IP, iface *net.Interface) error {
	mreq := windows.IPMreq{}
	copy(mreq.Multiaddr[:], mcastIP.To4())
	copy(mreq.Interface[:], net.IPv4zero.To4())
	return windows.SetsockoptIPMreq(s.handle, windows.IPPROTO_IP, windows.IP_DROP_MEMBERSHIP, &mreq)
}

func (s *WinSocket) SetMulticastTTL(ttl int) error {
	return windows.SetsockoptInt(s.handle, windows.IPPROTO_IP, windows.IP_MULTICAST_TTL, ttl)
}
