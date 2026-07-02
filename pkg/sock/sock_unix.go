//go:build !windows

package sock

import (
	"fmt"
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
	mreq := syscall.IPMreq{
		Multiaddr: [4]byte{mcastIP[0], mcastIP[1], mcastIP[2], mcastIP[3]},
	}

	var selectedIfName string
	var selectedIfIP net.IP

	if iface != nil {
		// 使用接口的第一个 IPv4 地址
		addrs, err := iface.Addrs()
		if err == nil && len(addrs) > 0 {
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
					copy(mreq.Interface[:], ipnet.IP.To4())
					selectedIfName = iface.Name
					selectedIfIP = ipnet.IP.To4()
					break
				}
			}
		}
	} else {
		// 没有指定接口时，自动找一个非回环、已启动的网卡
		// 优先选择以太网（en0 通常是 macOS 主网卡），避免选到 WiFi
		ifaces, err := net.Interfaces()
		if err == nil {
			// 打印所有可用接口，帮助诊断
			fmt.Printf("[multicast] Available interfaces:\n")
			for _, ifc := range ifaces {
				if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
					continue
				}
				addrs, _ := ifc.Addrs()
				for _, addr := range addrs {
					if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
						flags := ""
						if ifc.Flags&net.FlagRunning != 0 {
							flags += "RUNNING,"
						}
						if ifc.HardwareAddr != nil && len(ifc.HardwareAddr) > 0 {
							// 以太网和 WiFi 都有 MAC，但可以通过名字区分
						}
						fmt.Printf("[multicast]   %s: %s (%s)\n", ifc.Name, ipnet.IP.To4().String(), flags)
					}
				}
			}

			// 优先选择以太网接口（en0），其次选择其他非回环接口
			preferredOrder := []string{"en0", "eth0", "en1"}
			for _, prefName := range preferredOrder {
				for _, ifc := range ifaces {
					if ifc.Name != prefName {
						continue
					}
					if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
						continue
					}
					addrs, err := ifc.Addrs()
					if err != nil {
						continue
					}
					for _, addr := range addrs {
						if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
							copy(mreq.Interface[:], ipnet.IP.To4())
							selectedIfName = ifc.Name
							selectedIfIP = ipnet.IP.To4()
							break
						}
					}
					if selectedIfIP != nil {
						break
					}
				}
				if selectedIfIP != nil {
					break
				}
			}

			// 如果优先接口没找到，回退到第一个可用的非回环接口
			if selectedIfIP == nil {
				for _, ifc := range ifaces {
					if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
						continue
					}
					addrs, err := ifc.Addrs()
					if err != nil {
						continue
					}
					for _, addr := range addrs {
						if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
							copy(mreq.Interface[:], ipnet.IP.To4())
							selectedIfName = ifc.Name
							selectedIfIP = ipnet.IP.To4()
							break
						}
					}
					if selectedIfIP != nil {
						break
					}
				}
			}
		}
	}

	if mreq.Interface == [4]byte{0, 0, 0, 0} {
		return fmt.Errorf("no suitable interface found for multicast")
	}

	fmt.Printf("[multicast] Selected interface: %s (%s) for group %s\n",
		selectedIfName, selectedIfIP.String(), mcastIP.String())

	if err := syscall.SetsockoptIPMreq(s.fd, syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, &mreq); err != nil {
		return err
	}

	// 设置多播出口接口
	if err := syscall.SetsockoptInet4Addr(s.fd, syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, mreq.Interface); err != nil {
		return err
	}

	// 允许接收多播回环（本地发送端也能收到）
	if err := syscall.SetsockoptInt(s.fd, syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1); err != nil {
		return err
	}

	return nil
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
