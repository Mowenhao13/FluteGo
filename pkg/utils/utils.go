/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */
package utils

import (
	"FluteGo/constant"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/edsrzf/mmap-go"
)

// globalPeakHeapAlloc 记录全局堆内存分配的峰值（单位：字节）
// 通过原子操作保证并发访问的安全性
var globalPeakHeapAlloc uint64 // 全局峰值内存记录

// CalculateMd5 计算文件的 MD5 哈希值。
//
// # 描述
//
//	读取整个文件并计算其 MD5，适合对传输文件进行完整性校验。
//
// # 参数
//
//   - `file`: 需要计算哈希的文件对象
//
// # 返回值
//
//   - `string`: 32 位十六进制的 MD5 值
//   - `error`: 计算过程中发生的任何错误
//
// # 注意
//
//  1. 函数内部会将文件指针重置到开头
//  2. 函数不会在结束时恢复指针位置
func CalculateMd5(file *os.File) (string, error) {
	file.Seek(0, io.SeekStart)
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("计算MD5失败: %v", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// EnsureStaticARP 配置或移除静态 ARP 条目。
//
// # 描述
//
//	使用系统命令将 IP 和 MAC 永久绑定，以确保接收端与发送端在链路层不被篡改。
//
// # 参数
//
//   - `enable`: 是否启用静态 ARP
//   - `ip`, `mac`, `iface`: 要绑定的 IP, MAC 与网卡
//   - `role`: 日志中记录的角色描述
//
// # 返回值
//
//   - `error`: 失败时返回具体错误
//
// # 实现
//
//	通过执行 `ip neigh replace` 命令设置永久 ARP 映射
func EnsureStaticARP(enable bool, ip, mac, iface, role string) error {
	// 如果禁用静态ARP，直接返回
	if !enable {
		fmt.Printf("Static ARP disabled for %s\n", role)
		return nil
	}
	if ip == "" || mac == "" || iface == "" {
		return fmt.Errorf("missing ip (%s), mac (%s) or iface (%s) for static ARP", ip, mac, iface)
	}

	// 构建并执行ARP配置命令
	// 使用"replace"操作确保条目存在且为永久状态
	cmd := exec.Command("ip", "neigh", "replace", ip, "lladdr", mac, "nud", "permanent", "dev", iface)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return fmt.Errorf("ip neigh replace failed: %v (output: %s)", err, trimmed)
		}
		return fmt.Errorf("ip neigh replace failed: %v", err)
	}

	fmt.Printf("Static ARP configured for %s: %s -> %s via %s\n", role, ip, mac, iface)
	return nil
}

// ResolveMulticastInterface 根据多播 IP 查询路由表，自动确定出口网络接口和源 IP 地址。
//
// # 描述
//
//	通过操作系统路由表查询多播 IP 对应的出口接口，避免用户手动指定 --mcast-iface。
//	单向无反馈信道（FLUTE/UDP）一般运行在以太网上，路由表能正确指示 OS 选择哪个接口发送多播流量。
//
// # 平台支持
//
//   - darwin: route -n get <mcastIP> → interface: <name>
//   - linux:  ip route get <mcastIP> → dev <name> src <ip>
//
// # 参数
//
//   - `mcastIP`: 多播 IP 地址字符串（如 "239.1.1.1"）
//
// # 返回值
//
//   - `*net.Interface`: 找到的出口接口，查询失败时返回 nil
//   - `net.IP`: 该接口的源 IP 地址（IPv4），查询失败时返回 nil
//   - `error`: 查询过程中的错误，可用于日志排查
func ResolveMulticastInterface(mcastIP string) (*net.Interface, net.IP, error) {
	ip := net.ParseIP(mcastIP)
	if ip == nil {
		return nil, nil, fmt.Errorf("invalid multicast IP: %s", mcastIP)
	}

	var ifaceName string
	var srcIP net.IP

	switch runtime.GOOS {
	case "darwin":
		// macOS: route -n get <mcastIP>
		// 输出示例:
		//   route to: 239.1.1.1
		//   interface: en0
		//   ...
		out, err := exec.Command("route", "-n", "get", mcastIP).Output()
		if err != nil {
			return nil, nil, fmt.Errorf("route -n get %s failed: %w", mcastIP, err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "interface:") {
				ifaceName = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
				break
			}
		}
		if ifaceName == "" {
			return nil, nil, fmt.Errorf("route -n get %s: interface not found in output", mcastIP)
		}
		log.Printf("[multicast] route lookup: %s -> interface %s", mcastIP, ifaceName)

	case "linux":
		// Linux: ip route get <mcastIP>
		// 输出示例:
		//   multicast 239.1.1.1 dev eth0 src 192.168.0.12 table local ...
		out, err := exec.Command("ip", "route", "get", mcastIP).Output()
		if err != nil {
			return nil, nil, fmt.Errorf("ip route get %s failed: %w", mcastIP, err)
		}
		fields := strings.Fields(string(out))
		for i, f := range fields {
			if f == "dev" && i+1 < len(fields) {
				ifaceName = fields[i+1]
			}
			if f == "src" && i+1 < len(fields) {
				srcIP = net.ParseIP(fields[i+1])
			}
		}
		if ifaceName == "" {
			return nil, nil, fmt.Errorf("ip route get %s: 'dev' not found in output: %s", mcastIP, strings.TrimSpace(string(out)))
		}
		log.Printf("[multicast] route lookup: %s -> dev %s src %s", mcastIP, ifaceName, srcIP)

	default:
		return nil, nil, fmt.Errorf("ResolveMulticastInterface: unsupported platform %s", runtime.GOOS)
	}

	// 通过接口名查找 net.Interface
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, nil, fmt.Errorf("interface %s not found: %w", ifaceName, err)
	}

	// 如果路由表没给 src IP（macOS 场景），从接口获取第一个 IPv4 地址
	if srcIP == nil || srcIP.To4() == nil {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, nil, fmt.Errorf("get addresses for %s failed: %w", ifaceName, err)
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				if ipv4 := ipnet.IP.To4(); ipv4 != nil {
					srcIP = ipv4
					break
				}
			}
		}
		if srcIP == nil {
			return nil, nil, fmt.Errorf("interface %s has no IPv4 address", ifaceName)
		}
	}

	log.Printf("[multicast] resolved: %s via %s (%s, %s)", mcastIP, ifaceName, iface.Name, srcIP.String())
	return iface, srcIP, nil
}

// CreateUDPListener 在指定地址上创建 UDP 监听套接字。
//
// # 参数
//
//   - `sourceAddr`: 本地监听地址（`IP:Port` 或 `:Port`）。
//
// # 返回值
//
//   - `*net.UDPConn`: 成功创建的监听连接
//   - `error`: 解析地址或监听失败时返回错误
func CreateUDPListener(sourceAddr string) (*net.UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp", sourceAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve listen address failed: %v", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen UDP failed: %v", err)
	}

	return conn, nil
}

// CreateUDPConnection 建立到指定远程地址的 UDP 连接。
//
// # 参数
//
//   - `destAddr`: 目标地址（`IP:Port`）。
//
// # 返回值
//
//   - `*net.UDPConn`: 已连接的 UDP 套接字
//   - `error`: 解析地址或拨号失败时返回错误
func CreateUDPConnection(destAddr string) (*net.UDPConn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", destAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP address failed: %v", err)
	}

	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, fmt.Errorf("dial UDP failed: %v", err)
	}

	return conn, nil
}

// UpdatePeakMemory 记录当前堆内存分配的峰值。
//
// # 描述
//
//	读取 `runtime.ReadMemStats` 中的堆分配值，并通过原子操作更新全局峰值。
//
// # 线程安全
//
//	使用 `atomic.CompareAndSwapUint64` 进行无锁更新。
func UpdatePeakMemory() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	current := memStats.HeapAlloc

	// 原子操作更新峰值
	for {
		oldPeak := atomic.LoadUint64(&globalPeakHeapAlloc)
		// 如果当前值不大于旧峰值，则无需更新
		if current <= oldPeak {
			break
		}
		// 尝试原子更新，如果失败则重试
		if atomic.CompareAndSwapUint64(&globalPeakHeapAlloc, oldPeak, current) {
			break
		}
	}
}

// GetContentType 检测文件的内容类型（MIME 类型）。
//
// # 参数
//
//   - `file`: 待检测文件
//
// # 返回值
//
//   - `string`: 识别出的 MIME 类型
//
// # 实现
//
//	使用 `http.DetectContentType` 检测前 512 字节，并在无法检测时默认返回 `application/octet-stream`。
//
// # 注意
//
//  1. 函数会在开始和结束时将文件指针复位
//  2. 支持大多数常见文件类型
func GetContentType(file *os.File) string {
	// 重置文件指针到开头
	file.Seek(0, 0)

	// 读取前512字节用于检测（http.DetectContentType的要求）
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		// 默认类型
		return "application/octet-stream"
	}

	// 重置文件指针到开头（避免影响后续操作）
	file.Seek(0, 0)

	// 检测内容类型
	contentType := http.DetectContentType(buffer[:n])
	return contentType
}

// CopyFile 将源文件复制到目标路径。
//
// # 参数
//
//   - `src`: 源路径
//   - `dst`: 目标路径
//
// # 返回值
//
//   - `int64`: 实际复制的字节数
//   - `error`: I/O 或权限错误
//
// # 错误情境
//
//  1. 源文件不存在或非普通文件
//  2. 无法创建目标文件
//  3. 复制过程中发生 I/O 错误
func CopyFile(src, dst string) (int64, error) {
	// 检查源文件是否存在及其类型
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	// 确保源文件是普通文件
	if !sourceFileStat.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", src)
	}

	// 打开源文件
	source, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer source.Close()

	// 创建目标文件
	destination, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer destination.Close()

	// 执行文件复制操作
	nBytes, err := io.Copy(destination, source)
	return nBytes, err
}

// UnMmap 释放内存映射区域。
//
// # 参数
//
//   - `data`: 通过 mmap 创建的字节切片
//
// # 返回值
//
//	`error`: 无法解除映射时返回错误
func UnMmap(data []byte) error {
	dat := mmap.MMap(data)
	return dat.Unmap()
}

func SelectSendFileDir() string {
	if runtime.GOOS == "windows" {
		return constant.SendFileDir_win
	}

	return constant.SendFileDir_unix
}

func SelectSaveFileDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	subdir := constant.SaveFileDir_unix
	if runtime.GOOS == "windows" {
		subdir = constant.SaveFileDir_win
	}
	return filepath.Join(home, subdir)
}

// unix only
// func isWiredInterface(name string) bool {
// 	// 常见有线接口命名模式
// 	wiredPatterns := []string{
// 		"eth", "enp", "ens", "eno", "em", "p",
// 	}

// 	for _, pattern := range wiredPatterns {
// 		if strings.HasPrefix(name, pattern) {
// 			return true
// 		}
// 	}
// 	return false
// }

// func SetNetLink(dstIP, dstMac string) error {
// 	links, err := netlink.LinkList()
// 	if err != nil {
// 		return fmt.Errorf("Failed to get interfaces list: %v", err)
// 	}

// 	var ifaceName string
// 	var link netlink.Link
// 	for _, lk := range links {
// 		attrs := lk.Attrs()
// 		if lk.Type() == "device" {
// 			if isWiredInterface(attrs.Name) {
// 				ifaceName = attrs.Name
// 				link = lk
// 				break
// 			}
// 		}
// 	}

// 	err = netlink.LinkSetARPOff(link)
// 	if err != nil {
// 		return fmt.Errorf("Failed to set netlink %s arp off: %v", ifaceName, err)
// 	}

// 	neighMac, err := net.ParseMAC(dstMac)
// 	neigh := &netlink.Neigh{
// 		LinkIndex:    link.Attrs().Index,
// 		Family:       unix.AF_INET,
// 		State:        netlink.NUD_PERMANENT,
// 		IP:           net.ParseIP(dstIP),
// 		HardwareAddr: neighMac,
// 	}

// 	if err := netlink.NeighAdd(neigh); err != nil {
// 		return fmt.Errorf("Failed to add neighbor for netlink %s: %v", ifaceName, err)
// 	}

// 	return nil
// }

// windows only
func ListDir(path string) {
	entries, err := os.ReadDir(path)
	if err != nil {
		log.Printf("Failed to read directory: %v", err)
		return
	}

	fmt.Printf("\nDirectory listing for %s:\n", path)
	fmt.Printf("%-12s %-12s %s\n", "Mode", "Size", "Name")
	fmt.Println("----------------------------------------")

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fmt.Printf("%-12s %-12d %s\n", info.Mode(), info.Size(), info.Name())
	}
	fmt.Println()
}

// GetLocalIPv4 获取本机以太网接口上以 192.168 开头的 IPv4 地址。
//
// # 描述
//
//	仅扫描物理以太网接口，跳过虚拟网卡（Docker、Tailscale、VMware 等）。
//	优先返回 192.168.x.x 地址，若以太网接口无 192.168 地址则回退到
//	其他物理接口上的 192.168 地址，最后才考虑任意非虚拟接口的任何 IPv4。
//
// # 返回值
//
//   - `string`: 检测到的 IPv4 地址，如果失败返回 "127.0.0.1"
func GetLocalIPv4() string {
	// 只通过枚举接口获取，避免 UDP 拨号返回意外子网
	if ip := getLocalIPFromInterfaces(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

// getLocalIPByUDP 通过连接到外部地址获取本机 IP
func getLocalIPByUDP() string {
	// 连接到 Google DNS (不会真正建立连接)
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().String()
	// localAddr 格式是 "IP:Port"，需要提取 IP
	host, _, err := net.SplitHostPort(localAddr)
	if err != nil {
		return ""
	}

	// 验证不是回环地址
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() {
		return ""
	}

	return host
}

// getLocalIPFromInterfaces 通过枚举网络接口获取本地 IPv4 地址。
//
// 优先级：
//  1. 以太网接口上的 192.168.x.x（立即返回）
//  2. 其他物理接口上的 192.168.x.x
//  3. 以太网接口上的其他合法 IPv4
//  4. 其他物理接口上的其他合法 IPv4
func getLocalIPFromInterfaces() string {
	var ethCandidates, otherCandidates []string

	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	// 接口名中标识有线网的关键词
	ethNames := []string{"以太网", "ethernet", "eth", "enp", "enx",
		"enp0", "enp1", "enp2", "enp3", "enp4", "enp5",
		"eno", "ens", "wired"}

	isEthernetName := func(name string) bool {
		lower := strings.ToLower(name)
		for _, e := range ethNames {
			if strings.Contains(lower, e) {
				return true
			}
		}
		// macOS enX（en0/en5/en10）可能是物理网卡，不排除
		if strings.HasPrefix(lower, "en") && len(lower) <= 5 {
			return true
		}
		return false
	}

	for _, iface := range interfaces {
		// 跳过未启动的接口
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		// 跳过回环接口
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		// 跳过已知的虚拟网卡
		if isVirtualInterfaceName(iface.Name) {
			continue
		}

		// 获取接口的地址
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip == nil {
				continue
			}

			// 只使用 IPv4
			if ip.To4() == nil {
				continue
			}

			ipStr := ip.String()

			// 跳过回环地址
			if ip.IsLoopback() {
				continue
			}

			// 跳过链路本地地址 (169.254.x.x)
			if ip.IsLinkLocalUnicast() {
				continue
			}

			// 跳过已知虚拟 IP 段
			if isUndesiredIP(ipStr) {
				continue
			}

			if isEthernetName(iface.Name) {
				// 以太网接口上的 192.168.x.x → 立即返回
				if strings.HasPrefix(ipStr, "192.168.") {
					log.Printf("[GetLocalIPv4] found 192.168.x.x on ethernet %s: %s", iface.Name, ipStr)
					return ipStr
				}
				ethCandidates = append(ethCandidates, ipStr)
			} else {
				otherCandidates = append(otherCandidates, ipStr)
			}
		}
	}

	// 优先从以太网候选 IP 中选 192.168.x.x
	if len(ethCandidates) > 0 {
		for _, ip := range ethCandidates {
			if strings.HasPrefix(ip, "192.168.") {
				log.Printf("[GetLocalIPv4] found 192.168.x.x on ethernet (delayed): %s", ip)
				return ip
			}
		}
		// 没有 192.168 则返回以太网第一个合法 IP
		log.Printf("[GetLocalIPv4] no 192.168 on ethernet, using: %s", ethCandidates[0])
		return ethCandidates[0]
	}

	// 回退到其他物理接口
	if len(otherCandidates) > 0 {
		for _, ip := range otherCandidates {
			if strings.HasPrefix(ip, "192.168.") {
				log.Printf("[GetLocalIPv4] found 192.168.x.x on other interface: %s", ip)
				return ip
			}
		}
		log.Printf("[GetLocalIPv4] no 192.168 on any interface, using: %s", otherCandidates[0])
		return otherCandidates[0]
	}

	return ""
}

// isDockerIP 检查是否是 Docker 网络地址
func isDockerIP(ip string) bool {
	// Docker 常用网段: 172.17.x.x - 172.19.x.x
	return len(ip) >= 7 && ip[:7] == "172.17." ||
		(len(ip) >= 7 && ip[:7] == "172.18.") ||
		(len(ip) >= 7 && ip[:7] == "172.19.")
}

// isVMwareIP 检查是否是 VMware 网络地址
func isVMwareIP(ip string) bool {
	return len(ip) >= 12 && ip[:12] == "192.168.132." ||
		(len(ip) >= 12 && ip[:12] == "192.168.174.")
}

// selectPreferredIP 从候选 IP 中选择一个优先使用的
func selectPreferredIP(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}

	// 1. 优先选择 192.168.x.x (常见局域网)
	for _, ip := range candidates {
		if len(ip) >= 8 && ip[:8] == "192.168." {
			return ip
		}
	}

	// 2. 其次选择 10.x.x.x
	for _, ip := range candidates {
		if len(ip) >= 3 && ip[:3] == "10." {
			return ip
		}
	}

	// 3. 再次选择 172.16-31.x.x
	for _, ip := range candidates {
		if len(ip) >= 6 && ip[:3] == "172." {
			parts := net.ParseIP(ip).To4()
			if parts != nil && parts[1] >= 16 && parts[1] <= 31 {
				return ip
			}
		}
	}

	// 默认返回第一个
	return candidates[0]
}

// isUndesiredIP 检查 IP 是否属于虚拟网卡或不应使用的网络
func isUndesiredIP(ip string) bool {
	// 已知虚拟 IP 段
	blocks := []string{"100.", "26.", "25.", "172.17.", "172.18.",
		"172.19.", "192.168.132.", "192.168.174."}
	for _, b := range blocks {
		if strings.HasPrefix(ip, b) {
			return true
		}
	}
	// 检查 CGNAT (100.64.0.0/10) — Tailscale 常用
	if strings.HasPrefix(ip, "100.") {
		parts := net.ParseIP(ip).To4()
		if parts != nil && parts[1] >= 64 && parts[1] <= 127 {
			return true
		}
	}
	return false
}

// isVirtualInterfaceName 检查接口名是否为已知虚拟网卡
func isVirtualInterfaceName(name string) bool {
	names := []string{"tailscale", "radmin", "vmware", "virtualbox",
		"docker", "veth", "br-", "docker0", "tun", "tap",
		"utun", "awdl", "llw", "anpi", "vnic", "bridge"}
	lower := strings.ToLower(name)
	for _, v := range names {
		if strings.Contains(lower, v) {
			return true
		}
	}
	return false
}

func GetTransferringFiles(transferringFiles sync.Map) []string {
	var files []string
	transferringFiles.Range(func(key, value interface{}) bool {
		files = append(files, value.(string))
		return true
	})
	return files
}
