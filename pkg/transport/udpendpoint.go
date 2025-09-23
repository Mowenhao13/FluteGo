package transport

import (
	"net"
	"strconv"
)

type UDPEndpoint struct {
	// 可选：本地绑定地址（如 "0.0.0.0" 或具体网卡 IP）。nil 表示让内核自行选择。
	SourceAddress *string

	// 目的组播地址（或单播地址），例如 "224.0.0.1"
	DestinationGroupAddress string

	// 目的端口
	Port uint16
}

func NewUDPEndpoint(src *string, dest string, port uint16) UDPEndpoint {
	return UDPEndpoint{
		SourceAddress:           src,
		DestinationGroupAddress: dest,
		Port:                    port,
	}
}

// BindAddr 返回用于 net.ListenPacket("udp", BindAddr()) 的地址字符串。
// 若未指定 SourceAddress，则返回 ":<port>"，交给内核选择本地地址。
func (e UDPEndpoint) BindAddr() string {
	if e.SourceAddress == nil || *e.SourceAddress == "" {
		return net.JoinHostPort("", strconv.Itoa(int(e.Port)))
	}
	return net.JoinHostPort(*e.SourceAddress, strconv.Itoa(int(e.Port)))
}

// DestAddr 返回 "ip:port" 形式的目的地址，便于 net.ResolveUDPAddr 使用。
func (e UDPEndpoint) DestAddr() string {
	return net.JoinHostPort(e.DestinationGroupAddress, strconv.Itoa(int(e.Port)))
}

// ResolveDest 解析为 *net.UDPAddr
func (e UDPEndpoint) ResolveDest() (*net.UDPAddr, error) {
	return net.ResolveUDPAddr("udp", e.DestAddr())
}
