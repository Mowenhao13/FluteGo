// benchmark_udp_fixed.go
package test

import (
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/edsrzf/mmap-go"
	"golang.org/x/sys/windows"
)

const (
	PORT         = 9999
	PACKET_SIZE  = 1400
	BUFFER_SIZE  = 1400
	PACKET_COUNT = 10000

	TEST_FILE_PATH = "C:\\Users\\mowen\\Desktop\\FluteGo\\FluteGo\\cmd\\send_files\\S05E01.mp4"
)

// goos: windows
// goarch: amd64
// pkg: FluteGo/test/test_b
// cpu: AMD Ryzen 9 7940HX with Radeon Graphics
// === RUN   BenchmarkStdUDP
// BenchmarkStdUDP
// BenchmarkStdUDP-32
//
//	150602              8124 ns/op         120.20 MB/s        123087 packets/sec         4 B/op          1 allocs/op
func BenchmarkStdUDP(b *testing.B) {
	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: PORT}

	// 服务器
	serverConn, err := net.ListenUDP("udp", serverAddr)
	if err != nil {
		b.Fatal(err)
	}
	defer serverConn.Close()

	// 客户端
	clientConn, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		b.Fatal(err)
	}
	defer clientConn.Close()

	data := make([]byte, PACKET_SIZE)
	for i := range data {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()

	var wg sync.WaitGroup
	wg.Add(1)

	// 接收协程
	go func() {
		defer wg.Done()
		buf := make([]byte, BUFFER_SIZE)
		for i := 0; i < b.N; i++ {
			_, _, err := serverConn.ReadFromUDP(buf)
			if err != nil {
				b.Logf("接收错误: %v", err)
			}
		}
	}()

	// 发送测试
	for i := 0; i < b.N; i++ {
		_, err := clientConn.Write(data)
		if err != nil {
			b.Fatal(err)
		}
	}

	// 等待接收完成
	wg.Wait()

	b.StopTimer()

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "packets/sec")
	b.ReportMetric(float64(b.N*PACKET_SIZE)/b.Elapsed().Seconds()/1024/1024, "MB/s")
}

// goos: windows
// goarch: amd64
// pkg: FluteGo/test/test_b
// cpu: AMD Ryzen 9 7940HX with Radeon Graphics
// === RUN   BenchmarkXsysUDP
// BenchmarkXsysUDP
// BenchmarkXsysUDP-32
//
//	152589              8002 ns/op         122.04 MB/s        124971 packets/sec      152589 received           64 B/op          2 allocs/op
func BenchmarkXsysUDP(b *testing.B) {
	// 1. 获取唯一端口
	port := getAvailablePort()

	// 2. 创建服务器socket
	serverFd, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		b.Fatalf("创建服务器socket失败: %v", err)
	}
	defer windows.Closesocket(serverFd)

	// 3. Windows socket优化
	optimizeSocket(serverFd)

	// 4. 绑定服务器地址
	serverAddr := &windows.SockaddrInet4{Port: port}
	copy(serverAddr.Addr[:], []byte{127, 0, 0, 1})

	if err := windows.Bind(serverFd, serverAddr); err != nil {
		b.Fatalf("绑定服务器地址失败: %v", err)
	}

	// 5. 创建客户端socket
	clientFd, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		b.Fatalf("创建客户端socket失败: %v", err)
	}
	defer windows.Closesocket(clientFd)

	// 6. Windows socket优化
	optimizeSocket(clientFd)

	// 7. 目标地址
	destAddr := &windows.SockaddrInet4{Port: port}
	copy(destAddr.Addr[:], []byte{127, 0, 0, 1})

	// 8. 准备测试数据
	data := make([]byte, PACKET_SIZE)
	for i := range data {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()

	var wg sync.WaitGroup
	wg.Add(1)

	var received int32
	var stopFlag int32

	// 9. 接收协程
	go func() {
		defer wg.Done()

		buf := make([]byte, BUFFER_SIZE)

		for atomic.LoadInt32(&stopFlag) == 0 {
			// 使用非阻塞接收
			n, _, err := windows.Recvfrom(serverFd, buf, windows.MSG_PEEK)
			if err != nil {
				if err == windows.WSAEWOULDBLOCK {
					// 没有数据，短暂休眠
					time.Sleep(time.Microsecond)
					continue
				}
				b.Logf("接收错误: %v", err)
				continue
			}

			if n > 0 {
				// 实际接收数据
				n, from, err := windows.Recvfrom(serverFd, buf, 0)
				if err != nil {
					b.Logf("实际接收错误: %v", err)
					continue
				}

				atomic.AddInt32(&received, 1)

				// 避免编译器优化
				_ = n
				_ = from

				// 如果已收到足够数据，退出循环
				if atomic.LoadInt32(&received) >= int32(b.N) {
					break
				}
			}
		}
	}()

	// 10. 发送测试
	for i := 0; i < b.N; i++ {
		err := windows.Sendto(clientFd, data, 0, destAddr)
		if err != nil {
			b.Logf("发送失败: 尝试 %d/%d, 错误: %v",
				i+1, b.N, err)
		}
	}

	// 11. 等待接收完成
	timeout := time.After(5 * time.Second)
	for atomic.LoadInt32(&received) < int32(b.N) {
		select {
		case <-timeout:
			b.Logf("接收超时: 收到 %d/%d 个包", atomic.LoadInt32(&received), b.N)
			atomic.StoreInt32(&stopFlag, 1)
		default:
			time.Sleep(time.Microsecond)
		}
	}

	atomic.StoreInt32(&stopFlag, 1)
	wg.Wait()

	b.StopTimer()

	// 12. 性能报告
	elapsed := b.Elapsed().Seconds()
	packetsPerSec := float64(b.N) / elapsed
	throughputMBps := float64(b.N*PACKET_SIZE) / elapsed / 1024 / 1024

	b.ReportMetric(packetsPerSec, "packets/sec")
	b.ReportMetric(throughputMBps, "MB/s")
	b.ReportMetric(float64(atomic.LoadInt32(&received)), "received")
}

// 优化socket设置
func optimizeSocket(fd windows.Handle) error {
	// 1. 启用地址重用
	if err := windows.SetsockoptInt(fd, windows.SOL_SOCKET, windows.SO_REUSEADDR, 1); err != nil {
		return fmt.Errorf("设置SO_REUSEADDR失败: %w", err)
	}

	// 2. 设置发送缓冲区大小 (1MB)
	if err := windows.SetsockoptInt(fd, windows.SOL_SOCKET, windows.SO_SNDBUF, 1024*1024); err != nil {
		return fmt.Errorf("设置SO_SNDBUF失败: %w", err)
	}

	// 3. 设置接收缓冲区大小 (4MB)
	if err := windows.SetsockoptInt(fd, windows.SOL_SOCKET, windows.SO_RCVBUF, 4*1024*1024); err != nil {
		return fmt.Errorf("设置SO_RCVBUF失败: %w", err)
	}

	// 4. 禁用Nagle算法（对UDP无效，但保持模式）
	if err := windows.SetsockoptInt(fd, windows.IPPROTO_TCP, windows.TCP_NODELAY, 1); err != nil {
		// UDP socket可能返回错误，忽略
	}

	// 5. 设置非阻塞模式
	// 注意：Recvfrom在非阻塞模式下行为不同
	// 这里保持阻塞模式以获得准确性能比较

	return nil
}

// 获取可用端口
func getAvailablePort() int {
	// 使用标准库获取可用端口
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		return 20000 // 返回默认端口
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return 20000
	}
	defer conn.Close()

	return conn.LocalAddr().(*net.UDPAddr).Port
}

// BenchmarkStdUDPConcurrent 测试标准库UDP在高并发下的性能
// go test -bench=BenchmarkStdUDPConcurrent -benchmem -cpu=1,2,4,8,16

// goos: windows
// goarch: amd64
// pkg: FluteGo/test/test_b
// cpu: AMD Ryzen 9 7940HX with Radeon Graphics

// cpu=1
// BenchmarkStdUDPConcurrent         158941              7606 ns/op         128.39 MB/s        131473 packets/sec         3 B/op          0 allocs/op

// cpu=2
// BenchmarkStdUDPConcurrent-2       140076              8518 ns/op         114.65 MB/s        117405 packets/sec         4 B/op          1 allocs/op

// cpu=4
// BenchmarkStdUDPConcurrent-4       106693             10754 ns/op          90.81 MB/s         92987 packets/sec         4 B/op          1 allocs/op

// cpu=8
// BenchmarkStdUDPConcurrent-8       104684             10619 ns/op          91.97 MB/s         94175 packets/sec         4 B/op          1 allocs/op

// cpu=16
// BenchmarkStdUDPConcurrent-16              104880             10666 ns/op          91.56 MB/s         93758 packets/sec         4 B/op          1 allocs/op
func BenchmarkStdUDPConcurrent(b *testing.B) {
	// 1. 全局初始化数据 (只执行一次)
	initBenchmarkData()

	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: getAvailablePort()}

	// 服务器
	serverConn, err := net.ListenUDP("udp", serverAddr)
	if err != nil {
		b.Fatal(err)
	}
	defer serverConn.Close()

	// 客户端
	clientConn, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		b.Fatal(err)
	}
	defer clientConn.Close()

	// 优化缓冲区
	serverConn.SetReadBuffer(4 * 1024 * 1024)
	clientConn.SetWriteBuffer(1 * 1024 * 1024)

	numSymbols := len(benchSymbols)
	if numSymbols == 0 {
		b.Fatal("没有可发送的数据")
	}

	b.ResetTimer()

	var wg sync.WaitGroup
	wg.Add(1)
	var stopFlag int32

	// 接收协程
	go func() {
		defer wg.Done()
		buf := make([]byte, BUFFER_SIZE)
		for atomic.LoadInt32(&stopFlag) == 0 {
			serverConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			_, _, err := serverConn.ReadFromUDP(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				// b.Logf("接收错误: %v", err)
			}
		}
	}()

	// 并发发送测试
	b.RunParallel(func(pb *testing.PB) {
		idx := 0
		for pb.Next() {
			// 使用预处理好的全局数据
			symbol := benchSymbols[idx%numSymbols]
			idx++
			_, err := clientConn.Write(symbol)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	atomic.StoreInt32(&stopFlag, 1)
	wg.Wait()

	b.StopTimer()

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "packets/sec")
	b.ReportMetric(float64(b.N*1400)/b.Elapsed().Seconds()/1024/1024, "MB/s")
}

// 全局变量用于存储预处理的数据，避免每次Benchmark重复加载
var (
	benchData    mmap.MMap
	benchSymbols [][]byte
	benchOnce    sync.Once
)

// BenchmarkXsysUDPConcurrent 测试Windows原生Socket在高并发下的性能
// go test -bench=BenchmarkXsysUDPConcurrent -benchmem -cpu=1,2,4,8,16

// goos: windows
// goarch: amd64
// pkg: FluteGo/test/test_b
// cpu: AMD Ryzen 9 7940HX with Radeon Graphics

// cpu=1
// BenchmarkXsysUDPConcurrent        128181              9746 ns/op         100.20 MB/s        102605 packets/sec        31 B/op          0 allocs/op

// cpu=2
// BenchmarkXsysUDPConcurrent-2      197930              6433 ns/op         151.82 MB/s        155460 packets/sec        31 B/op          0 allocs/op

// cpu=4
// BenchmarkXsysUDPConcurrent-4      374299              3471 ns/op         281.34 MB/s        288097 packets/sec        31 B/op          0 allocs/op

// cpu=8
// BenchmarkXsysUDPConcurrent-8      533113              3017 ns/op         323.70 MB/s        331471 packets/sec        32 B/op          1 allocs/op

// cpu=16
// BenchmarkXsysUDPConcurrent-16             602479              1747 ns/op         558.99 MB/s        572404 packets/sec        28 B/op          0 allocs/op

// cpu=32
// BenchmarkXsysUDPConcurrent-32             584965              2219 ns/op         440.08 MB/s        450637 packets/sec        25 B/op          0 allocs/op
func BenchmarkXsysUDPConcurrent(b *testing.B) {
	// 1. 全局初始化数据 (只执行一次)
	initBenchmarkData()

	port := getAvailablePort()

	// 服务器
	serverFd, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		b.Fatalf("创建服务器socket失败: %v", err)
	}
	defer windows.Closesocket(serverFd)
	optimizeSocket(serverFd)

	serverAddr := &windows.SockaddrInet4{Port: port}
	copy(serverAddr.Addr[:], []byte{127, 0, 0, 1})
	if err := windows.Bind(serverFd, serverAddr); err != nil {
		b.Fatalf("绑定服务器地址失败: %v", err)
	}

	// 客户端
	clientFd, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		b.Fatalf("创建客户端socket失败: %v", err)
	}
	defer windows.Closesocket(clientFd)
	optimizeSocket(clientFd)

	destAddr := &windows.SockaddrInet4{Port: port}
	copy(destAddr.Addr[:], []byte{127, 0, 0, 1})

	numSymbols := len(benchSymbols)
	if numSymbols == 0 {
		b.Fatal("没有可发送的数据")
	}

	b.ResetTimer()

	var wg sync.WaitGroup
	wg.Add(1)
	var stopFlag int32

	// 接收协程
	go func() {
		defer wg.Done()
		buf := make([]byte, BUFFER_SIZE)

		// 提前设置超时，避免在循环中重复调用系统调用
		timeout := int32(100)
		windows.SetsockoptInt(serverFd, windows.SOL_SOCKET, windows.SO_RCVTIMEO, int(timeout))

		for atomic.LoadInt32(&stopFlag) == 0 {
			_, _, err := windows.Recvfrom(serverFd, buf, 0)
			if err != nil {
				// 检查是否是超时
				// Windows socket error 10060 is WSAETIMEDOUT
				if err == windows.WSAETIMEDOUT {
					continue
				}
				// b.Logf("接收错误: %v", err)
				continue
			}
		}
	}()

	// 并发发送测试
	b.RunParallel(func(pb *testing.PB) {
		idx := 0
		for pb.Next() {
			// 使用预处理好的全局数据
			symbol := benchSymbols[idx%numSymbols]
			idx++
			err := windows.Sendto(clientFd, symbol, 0, destAddr)
			if err != nil {
				b.Logf("发送失败: %v", err)
			}
		}
	})

	atomic.StoreInt32(&stopFlag, 1)
	wg.Wait()

	b.StopTimer()

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "packets/sec")
	b.ReportMetric(float64(b.N*1400)/b.Elapsed().Seconds()/1024/1024, "MB/s")
}

func initBenchmarkData() {
	benchOnce.Do(func() {
		file, err := os.Open(TEST_FILE_PATH)
		if err != nil {
			fmt.Printf("无法打开测试文件: %v, 将使用生成数据\n", err)
			// 如果文件不存在，生成100MB的虚拟数据
			benchData = make([]byte, 100*1024*1024)
		} else {
			// 注意：为了Benchmark性能，这里有意不关闭文件和Unmap，直到进程结束
			benchData, err = mmap.Map(file, mmap.RDONLY, 0)
			if err != nil {
				panic(fmt.Sprintf("内存映射文件失败: %v", err))
			}
		}

		// 切分数据为1400字节的symbol
		symbolSize := 1400
		// 预分配切片容量，避免append时的内存重新分配
		benchSymbols = make([][]byte, 0, len(benchData)/symbolSize+1)
		for i := 0; i < len(benchData); i += symbolSize {
			end := i + symbolSize
			if end > len(benchData) {
				end = len(benchData)
			}
			benchSymbols = append(benchSymbols, benchData[i:end])
		}
		fmt.Printf("Benchmark数据准备完成: %d symbols\n", len(benchSymbols))
	})
}

// TestXsysUDP_WholeFile 测试发送完整文件的吞吐量

// === RUN   TestXsysUDP_WholeFile
// Benchmark数据准备完成: 1862847 symbols
// 开始发送完整文件: 1862847 symbols, 2487.17 MB

// NumWorkers: 1
// go test -v -run=TestXsysUDP_WholeFile -cpu=1
// 发送完成: 耗时 18.6160679s, 吞吐量 133.60 MB/s

// NumWorkers: 4
// go test -v -run=TestXsysUDP_WholeFile -cpu=4
// 发送完成: 耗时 6.2218711s, 吞吐量 399.75 MB/s

// NumWorkers: 8
// go test -v -run=TestXsysUDP_WholeFile -cpu=8
// 发送完成: 耗时 5.3561242s, 吞吐量 464.36 MB/s

// NumWorkers: 16
// go test -v -run=TestXsysUDP_WholeFile -cpu=16
// 发送完成: 耗时 3.3702679s, 吞吐量 737.97 MB/s

// NumWorkers: 32
// go test -v -run=TestXsysUDP_WholeFile -cpu=32
// 发送完成: 耗时 3.7890616s, 吞吐量 656.41 MB/s

func TestXsysUDP_WholeFile(t *testing.T) {
	initBenchmarkData()

	port := getAvailablePort()

	// 服务器
	serverFd, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		t.Fatalf("创建服务器socket失败: %v", err)
	}
	defer windows.Closesocket(serverFd)
	optimizeSocket(serverFd)

	serverAddr := &windows.SockaddrInet4{Port: port}
	copy(serverAddr.Addr[:], []byte{127, 0, 0, 1})
	if err := windows.Bind(serverFd, serverAddr); err != nil {
		t.Fatalf("绑定服务器地址失败: %v", err)
	}

	// 客户端
	clientFd, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		t.Fatalf("创建客户端socket失败: %v", err)
	}
	defer windows.Closesocket(clientFd)
	optimizeSocket(clientFd)

	destAddr := &windows.SockaddrInet4{Port: port}
	copy(destAddr.Addr[:], []byte{127, 0, 0, 1})

	totalSymbols := uint64(len(benchSymbols))
	if totalSymbols == 0 {
		t.Fatal("没有可发送的数据")
	}

	fmt.Printf("开始发送完整文件: %d symbols, %.2f MB\n", totalSymbols, float64(totalSymbols*1400)/1024/1024)

	var wg sync.WaitGroup
	wg.Add(1)
	var stopFlag int32

	// 接收协程
	go func() {
		defer wg.Done()
		buf := make([]byte, BUFFER_SIZE)
		timeout := int32(100)
		windows.SetsockoptInt(serverFd, windows.SOL_SOCKET, windows.SO_RCVTIMEO, int(timeout))

		for atomic.LoadInt32(&stopFlag) == 0 {
			_, _, err := windows.Recvfrom(serverFd, buf, 0)
			if err != nil {
				continue
			}
		}
	}()

	// 发送完整文件 (多协程协作)
	numWorkers := 16
	var sentIdx uint64
	var sendWg sync.WaitGroup

	start := time.Now()

	for i := 0; i < numWorkers; i++ {
		sendWg.Add(1)
		go func() {
			defer sendWg.Done()
			for {
				idx := atomic.AddUint64(&sentIdx, 1) - 1
				if idx >= totalSymbols {
					return
				}
				err := windows.Sendto(clientFd, benchSymbols[idx], 0, destAddr)
				if err != nil {
					// t.Logf("发送失败: %v", err)
				}
			}
		}()
	}

	sendWg.Wait()
	duration := time.Since(start)

	atomic.StoreInt32(&stopFlag, 1)
	wg.Wait()

	mbps := float64(totalSymbols*1400) / duration.Seconds() / 1024 / 1024
	fmt.Printf("发送完成: 耗时 %v, 吞吐量 %.2f MB/s\n", duration, mbps)
}

// TestStdUDP_WholeFile 测试标准库UDP发送完整文件的吞吐量
// go test -v -run=TestStdUDP_WholeFile -cpu=4
// === RUN   TestStdUDP_WholeFile
// Benchmark数据准备完成: 1862847 symbols
// 开始发送完整文件(StdUDP): 1862847 symbols, 2487.17 MB
// 发送完成(StdUDP): 耗时 19.2828557s, 吞吐量 128.98 MB/s
// --- PASS: TestStdUDP_WholeFile (19.40s)
// PASS
// ok      FluteGo/test/test_b     20.049s
func TestStdUDP_WholeFile(t *testing.T) {
	initBenchmarkData()

	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: getAvailablePort()}

	// 服务器
	serverConn, err := net.ListenUDP("udp", serverAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.Close()

	// 客户端
	clientConn, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	// 优化缓冲区
	serverConn.SetReadBuffer(4 * 1024 * 1024)
	clientConn.SetWriteBuffer(1 * 1024 * 1024)

	totalSymbols := uint64(len(benchSymbols))
	if totalSymbols == 0 {
		t.Fatal("没有可发送的数据")
	}

	fmt.Printf("开始发送完整文件(StdUDP): %d symbols, %.2f MB\n", totalSymbols, float64(totalSymbols*1400)/1024/1024)

	var wg sync.WaitGroup
	wg.Add(1)
	var stopFlag int32

	// 接收协程
	go func() {
		defer wg.Done()
		buf := make([]byte, BUFFER_SIZE)
		for atomic.LoadInt32(&stopFlag) == 0 {
			serverConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			_, _, err := serverConn.ReadFromUDP(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
			}
		}
	}()

	// 发送完整文件 (多协程协作)
	numWorkers := 8
	var sentIdx uint64
	var sendWg sync.WaitGroup

	start := time.Now()

	for i := 0; i < numWorkers; i++ {
		sendWg.Add(1)
		go func() {
			defer sendWg.Done()
			for {
				idx := atomic.AddUint64(&sentIdx, 1) - 1
				if idx >= totalSymbols {
					return
				}
				_, err := clientConn.Write(benchSymbols[idx])
				if err != nil {
					// t.Logf("发送失败: %v", err)
				}
			}
		}()
	}

	sendWg.Wait()
	duration := time.Since(start)

	atomic.StoreInt32(&stopFlag, 1)
	wg.Wait()

	mbps := float64(totalSymbols*1400) / duration.Seconds() / 1024 / 1024
	fmt.Printf("发送完成(StdUDP): 耗时 %v, 吞吐量 %.2f MB/s\n", duration, mbps)
}

// TestWSAXsysUDP_WholeFile 测试使用WSA系列函数发送完整文件的吞吐量

// NumWorkers: 16
// go test -v -run=TestWSAXsysUDP_WholeFile -cpu=16
// 发送完成(WSA): 耗时 3.3509058s, 吞吐量 742.24 MB/s

func TestWSAXsysUDP_WholeFile(t *testing.T) {
	initBenchmarkData()

	port := getAvailablePort()

	// 服务器
	serverFd, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		t.Fatalf("创建服务器socket失败: %v", err)
	}
	defer windows.Closesocket(serverFd)
	optimizeSocket(serverFd)

	serverAddr := &windows.SockaddrInet4{Port: port}
	copy(serverAddr.Addr[:], []byte{127, 0, 0, 1})
	if err := windows.Bind(serverFd, serverAddr); err != nil {
		t.Fatalf("绑定服务器地址失败: %v", err)
	}

	// 客户端
	clientFd, err := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
	if err != nil {
		t.Fatalf("创建客户端socket失败: %v", err)
	}
	defer windows.Closesocket(clientFd)
	optimizeSocket(clientFd)

	// 准备目标地址 (RawSockaddrAny for WSASendTo)
	var to windows.RawSockaddrInet4
	to.Family = windows.AF_INET
	nPort := uint16(port)
	to.Port = (nPort<<8)&0xff00 | (nPort>>8)&0x00ff // htons
	copy(to.Addr[:], []byte{127, 0, 0, 1})
	toAny := (*windows.RawSockaddrAny)(unsafe.Pointer(&to))
	toLen := int32(unsafe.Sizeof(to))

	totalSymbols := uint64(len(benchSymbols))
	if totalSymbols == 0 {
		t.Fatal("没有可发送的数据")
	}

	fmt.Printf("开始发送完整文件(WSA): %d symbols, %.2f MB\n", totalSymbols, float64(totalSymbols*1400)/1024/1024)

	var wg sync.WaitGroup
	wg.Add(1)
	var stopFlag int32

	// 接收协程
	go func() {
		defer wg.Done()
		buf := make([]byte, BUFFER_SIZE)

		// WSARecvFrom 参数准备
		var wsaBuf windows.WSABuf
		wsaBuf.Len = uint32(len(buf))
		wsaBuf.Buf = &buf[0]
		var bytesRecvd uint32
		var flags uint32
		var from windows.RawSockaddrAny
		var fromLen int32 = int32(unsafe.Sizeof(from))

		timeout := int32(100)
		windows.SetsockoptInt(serverFd, windows.SOL_SOCKET, windows.SO_RCVTIMEO, int(timeout))

		for atomic.LoadInt32(&stopFlag) == 0 {
			// 重置参数
			flags = 0
			fromLen = int32(unsafe.Sizeof(from))

			err := windows.WSARecvFrom(serverFd, &wsaBuf, 1, &bytesRecvd, &flags, &from, &fromLen, nil, nil)
			if err != nil {
				continue
			}
		}
	}()

	// 发送完整文件 (多协程协作)
	numWorkers := 256
	var sentIdx uint64
	var sendWg sync.WaitGroup

	start := time.Now()

	for i := 0; i < numWorkers; i++ {
		sendWg.Add(1)
		go func() {
			defer sendWg.Done()

			// 每个worker需要自己的WSABuf结构
			var wsaBuf windows.WSABuf
			var bytesSent uint32

			for {
				idx := atomic.AddUint64(&sentIdx, 1) - 1
				if idx >= totalSymbols {
					return
				}

				data := benchSymbols[idx]
				wsaBuf.Len = uint32(len(data))
				wsaBuf.Buf = &data[0]

				err := windows.WSASendTo(clientFd, &wsaBuf, 1, &bytesSent, 0, toAny, toLen, nil, nil)
				if err != nil {
					// t.Logf("发送失败: %v", err)
				}
			}
		}()
	}

	sendWg.Wait()
	duration := time.Since(start)

	atomic.StoreInt32(&stopFlag, 1)
	wg.Wait()

	mbps := float64(totalSymbols*1400) / duration.Seconds() / 1024 / 1024
	fmt.Printf("发送完成(WSA): 耗时 %v, 吞吐量 %.2f MB/s\n", duration, mbps)
}
