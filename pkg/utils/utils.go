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
	if runtime.GOOS == "windows" {
		return constant.SaveFileDir_win
	}
	return constant.SaveFileDir_unix
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

// GetLocalIPv4 获取本机的以太网 IPv4 地址
//
// # 描述
//
//	优先返回局域网地址 (192.168.x.x, 10.x.x.x, 172.16-31.x.x)
//	跳过回环地址 (127.x.x.x) 和 Docker/VMware/Tailscale 等虚拟网络
//
// # 返回值
//
//   - `string`: 检测到的 IPv4 地址，如果失败返回 "127.0.0.1"
func GetLocalIPv4() string {
	// 方法 1: 通过连接到外部地址获取（最快）
	if ip := getLocalIPByUDP(); ip != "" && !isUndesiredIP(ip) {
		return ip
	}

	// 方法 2: 枚举所有网络接口
	if ip := getLocalIPFromInterfaces(); ip != "" {
		return ip
	}

	// 默认返回回环地址
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

// getLocalIPFromInterfaces 通过枚举网络接口获取 IP
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
				// 排除 macOS 的 en0/en1 等（通常是 Wi-Fi）
				if strings.HasPrefix(lower, "en") && len(lower) <= 3 {
					continue
				}
				return true
			}
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
				ethCandidates = append(ethCandidates, ipStr)
			} else {
				otherCandidates = append(otherCandidates, ipStr)
			}
		}
	}

	// 优先从有线网卡候选 IP 中选，回退到其他候选
	if len(ethCandidates) > 0 {
		return selectPreferredIP(ethCandidates)
	}
	return selectPreferredIP(otherCandidates)
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
		"utun", "awdl", "llw", "anpi", "vnic"}
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
