/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */
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
	"golang.org/x/sys/windows"
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
func CreateSocket(ip string, port int) (windows.Handle, error) {
	sock, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		return 0, fmt.Errorf("Create UDP socket failed: %v", err)
	}

	if err := windows.SetsockoptInt(sock, windows.SOL_SOCKET, windows.SO_REUSEADDR, 1); err != nil {
		windows.CloseHandle(sock)
		return 0, fmt.Errorf("Set SO_REUSEADDR failed: %v", err)
	}

	if err := windows.SetsockoptInt(sock, windows.SOL_SOCKET, windows.SO_SNDBUF, constant.TX_BUF); err != nil {
		windows.CloseHandle(sock)
		return 0, fmt.Errorf("Set SO_SNDBUF failed: %v", err)
	}

	if err := windows.SetsockoptInt(sock, windows.SOL_SOCKET, windows.SO_RCVBUF, constant.RX_BUF); err != nil {
		windows.CloseHandle(sock)
		return 0, fmt.Errorf("Set SO_RCVBUF failed: %v", err)
	}

	if err := windows.SetsockoptInt(sock, windows.IPPROTO_UDP, windows.TCP_NODELAY, 1); err != nil {
		windows.CloseHandle(sock)
		return 0, fmt.Errorf("Set TCP_NODELAY failed: %v", err)
	}

	sockaddr := &windows.SockaddrInet4{Port: port}
	ipAddr := net.ParseIP(ip).To4()
	copy(sockaddr.Addr[:], ipAddr)
	if err := windows.Bind(sock, sockaddr); err != nil {
		windows.CloseHandle(sock)
		return 0, fmt.Errorf("Bind socket failed: %v", err)
	}

	return sock, nil
}

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

// func GetRateLimit() int {

// }

func GetTransferringFiles(transferringFiles sync.Map) []string {
	var files []string
	transferringFiles.Range(func(key, value interface{}) bool {
		files = append(files, value.(string))
		return true
	})
	return files
}
