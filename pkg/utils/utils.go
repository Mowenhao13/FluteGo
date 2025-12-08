/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package utils

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/edsrzf/mmap-go"
)

// globalPeakHeapAlloc 记录全局堆内存分配的峰值（单位：字节）
// 通过原子操作保证并发访问的安全性
var globalPeakHeapAlloc uint64 // 全局峰值内存记录


// CalculateMd5 计算文件的MD5哈希值
// 参数：
//   file - 要计算哈希的文件指针
// 返回值：
//   string - 计算得到的32位十六进制MD5哈希值
//   error - 如果计算过程中发生错误，则返回错误信息
// 注意事项：
//   1. 函数会自动将文件指针重置到文件开头
//   2. 计算完成后文件指针不会重置，调用方需自行处理
func CalculateMd5(file *os.File) (string, error) {
	file.Seek(0, io.SeekStart)
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("计算MD5失败: %v", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// EnsureStaticARP 配置或移除静态ARP条目
// 功能说明：
//   在指定的网络接口上创建永久静态ARP条目，将IP地址映射到指定的MAC地址
// 参数：
//   enable - 启用或禁用静态ARP配置
//   ip     - 目标IP地址
//   mac    - 目标MAC地址
//   iface  - 网络接口名称
//   role   - ARP条目的描述信息（用于日志输出）
// 返回值：
//   error - 配置成功返回nil，否则返回错误信息
// 实现原理：
//   通过执行"ip neigh replace"命令创建永久静态ARP条目
// 使用场景：
//   在需要固定IP-MAC映射的网络环境中使用，如防止ARP欺骗攻击
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

// CreateUDPListener 创建UDP监听套接字
// 功能说明：
//   在指定的本地地址和端口上创建UDP监听套接字
// 参数：
//   sourceAddr - 监听地址，格式为"IP:Port"或":Port"
// 返回值：
//   *net.UDPConn - 创建成功的UDP连接对象
//   error - 创建失败时返回错误信息
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

// CreateUDPConnection 创建UDP连接套接字
// 功能说明：
//   创建连接到指定远程地址的UDP套接字
// 参数：
//   destAddr - 目标地址，格式为"IP:Port"
// 返回值：
//   *net.UDPConn - 创建成功的UDP连接对象
//   error - 创建失败时返回错误信息
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

// UpdatePeakMemory 更新峰值内存使用量
// 功能说明：
//   读取当前堆内存分配情况，更新全局峰值内存记录
// 实现原理：
//   1. 通过runtime.ReadMemStats获取内存统计信息
//   2. 使用原子操作更新全局峰值，确保线程安全
// 线程安全：
//   使用atomic.CompareAndSwapUint64实现无锁的原子更新
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

// GetContentType 检测文件的内容类型（MIME类型）
// 功能说明：
//   通过读取文件的前512字节，检测文件的MIME类型
// 参数：
//   file - 要检测的文件指针
// 返回值：
//   string - 检测到的MIME类型字符串
// 实现原理：
//   基于http.DetectContentType实现，遵循MIME类型检测标准
// 注意事项：
//   1. 函数会自动重置文件指针
//   2. 对于无法识别的类型，返回"application/octet-stream"
//   3. 只读取前512字节，适合大多数文件类型检测
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

// CopyFile 复制文件
// 功能说明：
//   将源文件完整复制到目标路径
// 参数：
//   src - 源文件路径
//   dst - 目标文件路径
// 返回值：
//   int64 - 实际复制的字节数
//   error - 复制过程中的错误信息
// 错误处理：
//   1. 源文件不存在或无权限访问
//   2. 源文件不是普通文件（如目录、设备文件等）
//   3. 目标文件创建失败或无写入权限
//   4. 复制过程中发生I/O错误
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

func UnMmap(data []byte) error {
	dat := mmap.MMap(data)
	return dat.Unmap()
}
