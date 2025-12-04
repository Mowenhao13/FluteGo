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
)

var globalPeakHeapAlloc uint64 // 全局峰值内存记录

func CalculateMd5(file *os.File) (string, error) {
	file.Seek(0, io.SeekStart)
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("计算MD5失败: %v", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func EnsureStaticARP(enable bool, ip, mac, iface, role string) error {
	if !enable {
		fmt.Printf("Static ARP disabled for %s\n", role)
		return nil
	}
	if ip == "" || mac == "" || iface == "" {
		return fmt.Errorf("missing ip (%s), mac (%s) or iface (%s) for static ARP", ip, mac, iface)
	}

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


// 更新峰值内存的函数
func UpdatePeakMemory() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	current := memStats.HeapAlloc
	
	// 原子操作更新峰值
	for {
		oldPeak := atomic.LoadUint64(&globalPeakHeapAlloc)
		if current <= oldPeak {
			break
		}
		if atomic.CompareAndSwapUint64(&globalPeakHeapAlloc, oldPeak, current) {
			break
		}
	}
}

func GetContentType(file *os.File) string {
    // 重置文件指针到开头
    file.Seek(0, 0)
    
    // 读取前512字节用于检测（http.DetectContentType的要求）
    buffer := make([]byte, 512)
    n, err := file.Read(buffer)
    if err != nil && err != io.EOF {
        return "application/octet-stream" // 默认类型
    }
    
    // 重置文件指针到开头（避免影响后续操作）
    file.Seek(0, 0)
    
    // 检测内容类型
    contentType := http.DetectContentType(buffer[:n])
    return contentType
}

func CopyFile(src, dst string) (int64, error) {
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if !sourceFileStat.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", src)
	}

	source, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer destination.Close()

	nBytes, err := io.Copy(destination, source)
	return nBytes, err
}