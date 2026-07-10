//go:build windows
// +build windows

package sock

import (
	"fmt"
	"log"
	"net"
	"strings"
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

	// 禁用 Windows UDP 的 WSAECONNRESET 行为（关键修复）
	// Windows 上，当 UDP 发送触发 ICMP Port Unreachable 时（接收端未就绪、防火墙拦截等），
	// 后续的 WSASendTo/WSARecvFrom 会返回 WSAECONNRESET(10054) 错误。
	// 这会导致大文件传输中途中断（发送端 fatalSendErr=1 后停止编码）。
	// Unix 没有此问题。通过 SIO_UDP_CONNRESET ioctl 禁用此行为。
	var connResetEnabled uint32 = 0 // FALSE = 禁用 CONNRESET 上报
	var bytesReturned uint32
	if err := windows.WSAIoctl(sock, windows.SIO_UDP_CONNRESET, (*byte)(unsafe.Pointer(&connResetEnabled)), uint32(unsafe.Sizeof(connResetEnabled)), nil, 0, &bytesReturned, nil, 0); err != nil {
		log.Printf("[sock_win] Warning: SIO_UDP_CONNRESET ioctl failed: %v", err)
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

	// 设置默认接收超时（100ms），使 WSARecvFrom 不会永久阻塞，
	// 让读取循环能定期检查 ctx.Done() 实现优雅退出。
	// Windows 的 SO_RCVTIMEO 接受毫秒级 DWORD（而非 timeval）。
	if err := windows.SetsockoptInt(sock, windows.SOL_SOCKET, SO_RCVTIMEO, 100); err != nil {
		// 超时设置失败不致命，仅记录日志
		log.Printf("[sock_win] set SO_RCVTIMEO=100ms failed: %v", err)
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
	// Windows 的 SO_RCVTIMEO 接受毫秒级 DWORD（而非 timeval）。
	// 将绝对时间点 t 转换为相对超时毫秒数。
	var timeoutMs int32
	if t.IsZero() {
		timeoutMs = 0 // 取消超时，阻塞读取
	} else {
		d := time.Until(t)
		if d < 0 {
			timeoutMs = 1 // 已过期，立即超时
		} else {
			timeoutMs = int32(d / time.Millisecond)
			if timeoutMs <= 0 {
				timeoutMs = 1
			}
		}
	}
	return windows.SetsockoptInt(s.handle, windows.SOL_SOCKET, SO_RCVTIMEO, int(timeoutMs))
}

func (s *WinSocket) SetWriteDeadline(t time.Time) error {
	// Windows 的 SO_SNDTIMEO 接受毫秒级 DWORD。
	var timeoutMs int32
	if t.IsZero() {
		timeoutMs = 0
	} else {
		d := time.Until(t)
		if d < 0 {
			timeoutMs = 1
		} else {
			timeoutMs = int32(d / time.Millisecond)
			if timeoutMs <= 0 {
				timeoutMs = 1
			}
		}
	}
	return windows.SetsockoptInt(s.handle, windows.SOL_SOCKET, SO_SNDTIMEO, int(timeoutMs))
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

	// RawSockaddrInet4.Port 必须使用网络字节序（big-endian），与 sockaddr_in 一致。
	// 这里手动将主机字节序的 port 转换为网络字节序。
	// 注意：windows.SockaddrInet4.Port 使用主机字节序（Bind 时内核自动转换），
	// 但 RawSockaddrInet4 直接传给 WSASendTo，必须已是网络字节序。
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
		// 让 Windows 自动选择默认路由接口（比用关键字猜接口更可靠）
		logMulticastInterfaces()
		joinIP = net.IPv4zero
		ifName = "INADDR_ANY (auto)"
		log.Printf("[multicast] WARNING: no specific interface selected, using INADDR_ANY")
		log.Printf("[multicast] HINT: set 'multicastIfaceIP' in config to the receiver's own LAN IP (e.g. 192.168.0.12)")
	}

	// 加入多播组
	mreq := windows.IPMreq{}
	copy(mreq.Multiaddr[:], mcastIP.To4())
	copy(mreq.Interface[:], joinIP.To4())
	if err := windows.SetsockoptIPMreq(s.handle, windows.IPPROTO_IP, windows.IP_ADD_MEMBERSHIP, &mreq); err != nil {
		return fmt.Errorf("join multicast %s on %s failed: %w", mcastIP.String(), ifName, err)
	}
	log.Printf("[multicast] joined group %s on interface %s (%s)", mcastIP.String(), joinIP.String(), ifName)

	// 设置多播出口接口（仅当指定了具体接口时；INADDR_ANY 时让系统用默认路由）
	if joinIP[0] != 0 || joinIP[1] != 0 || joinIP[2] != 0 || joinIP[3] != 0 {
		ifaceBytes := [4]byte{joinIP[0], joinIP[1], joinIP[2], joinIP[3]}
		if err := windows.SetsockoptInet4Addr(s.handle, windows.IPPROTO_IP, windows.IP_MULTICAST_IF, ifaceBytes); err != nil {
			log.Printf("[multicast] set IP_MULTICAST_IF to %s failed: %v", joinIP.String(), err)
		}
	}

	// 允许接收多播回环（与 Unix 版本保持一致）
	if err := windows.SetsockoptInt(s.handle, windows.IPPROTO_IP, windows.IP_MULTICAST_LOOP, 1); err != nil {
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

func (s *WinSocket) LeaveMulticastGroup(mcastIP net.IP, iface *net.Interface) error {
	mreq := windows.IPMreq{}
	copy(mreq.Multiaddr[:], mcastIP.To4())
	copy(mreq.Interface[:], net.IPv4zero.To4())
	return windows.SetsockoptIPMreq(s.handle, windows.IPPROTO_IP, windows.IP_DROP_MEMBERSHIP, &mreq)
}

func (s *WinSocket) SetMulticastTTL(ttl int) error {
	return windows.SetsockoptInt(s.handle, windows.IPPROTO_IP, windows.IP_MULTICAST_TTL, ttl)
}
