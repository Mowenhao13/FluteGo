package test

// import (
// 	"FluteGo/pkg/utils"
// 	"crypto/md5"
// 	"encoding/hex"
// 	"fmt"
// 	"net/http"
// 	"os"
// 	"runtime"
// 	"runtime/debug"
// 	"runtime/pprof"
// 	"strings"
// 	"testing"
// 	"golang.org/x/sys/unix"
// )

// const filePath = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1024mb.bin"

// func calculateMD5(data []byte) string {
// 	hash := md5.Sum(data)
// 	return hex.EncodeToString(hash[:])
// }

// func init() {
// 	// 启动pprof服务器
// 	go func() {
// 		fmt.Println("Starting pprof server on :6060")
// 		http.ListenAndServe("localhost:6060", nil)
// 	}()
// }

// // 内存分析辅助函数
// func startMemoryProfile(profileName string) func() {
// 	// 强制进行GC，获取干净的内存状态
// 	runtime.GC()
// 	debug.FreeOSMemory()

// 	// 开始内存分析
// 	f, err := os.Create(profileName)
// 	if err != nil {
// 		panic(err)
// 	}

// 	// 记录开始时的内存统计
// 	var mstats runtime.MemStats
// 	runtime.ReadMemStats(&mstats)
// 	fmt.Printf("开始内存分析 - 分配内存: %.2f MB\n", float64(mstats.Alloc)/1024/1024)

// 	return func() {
// 		pprof.WriteHeapProfile(f)
// 		f.Close()

// 		runtime.ReadMemStats(&mstats)
// 		fmt.Printf("结束内存分析 - 分配内存: %.2f MB\n", float64(mstats.Alloc)/1024/1024)
// 	}
// }

// func TestReadFile(t *testing.T) {
// 	fmt.Println("=== 测试 os.ReadFile 方式 ===")

// 	stopProfiling := startMemoryProfile("mem_readfile.prof")
// 	defer stopProfiling()

// 	file, err := os.ReadFile(filePath)
// 	if err != nil {
// 		t.Errorf("Failed to read file: %v", err)
// 		return
// 	}

// 	md5Sum := calculateMD5(file)

// 	// 打印内存信息
// 	var mstats runtime.MemStats
// 	runtime.ReadMemStats(&mstats)
// 	fmt.Printf("文件大小: %d bytes (%.2f MB)\n", len(file), float64(len(file))/1024/1024)
// 	fmt.Printf("峰值内存: %.2f MB\n", float64(mstats.Alloc)/1024/1024)
// 	fmt.Printf("MD5: %s\n", md5Sum)
// }

// func TestOpenFile(t *testing.T) {
// 	fmt.Println("=== 测试 os.Open + 流式处理方式 ===")

// 	stopProfiling := startMemoryProfile("mem_streaming.prof")
// 	defer stopProfiling()

// 	file, err := os.Open(filePath)
// 	if err != nil {
// 		t.Errorf("Failed to open file: %v", err)
// 		return
// 	}
// 	defer file.Close()

// 	info, err := file.Stat()
// 	if err != nil {
// 		t.Errorf("Failed to get file info: %v", err)
// 		return
// 	}

// 	fileSize := info.Size()
// 	md5sum, err := utils.CalculateMd5(file)
// 	if err != nil {
// 		t.Errorf("Failed to calculate MD5: %v", err)
// 		return
// 	}

// 	// 打印内存信息
// 	var mstats runtime.MemStats
// 	runtime.ReadMemStats(&mstats)
// 	fmt.Printf("文件大小: %d bytes (%.2f MB)\n", fileSize, float64(fileSize)/1024/1024)
// 	fmt.Printf("峰值内存: %.2f MB\n", float64(mstats.Alloc)/1024/1024)
// 	fmt.Printf("MD5: %s\n", md5sum)
// }

// func TestOpenFileMmap(t *testing.T) {
// 	fmt.Println("=== 测试 MMAP 内存映射方式 ===")

// 	stopProfiling := startMemoryProfile("mem_mmap.prof")
// 	defer stopProfiling()

// 	file, err := os.Open(filePath)
// 	if err != nil {
// 		t.Errorf("Failed to open file: %v", err)
// 		return
// 	}
// 	defer file.Close()

// 	info, err := file.Stat()
// 	if err != nil {
// 		t.Errorf("Failed to get file info: %v", err)
// 		return
// 	}
// 	fileSize := info.Size()

// 	fd := int(file.Fd())
// 	data, err := unix.Mmap(fd, 0, int(fileSize), unix.PROT_READ, unix.MAP_SHARED)
// 	if err != nil {
// 		t.Errorf("MMAP failed: %v", err)
// 		return
// 	}
// 	defer unix.Munmap(data)
// 	// data[0] = 1
// 	md5sum := calculateMD5(data)

// 	// 打印内存信息
// 	var mstats runtime.MemStats
// 	runtime.ReadMemStats(&mstats)
// 	fmt.Printf("文件大小: %d bytes (%.2f MB)\n", fileSize, float64(fileSize)/1024/1024)
// 	fmt.Printf("峰值内存: %.2f MB\n", float64(mstats.Alloc)/1024/1024)
// 	fmt.Printf("MD5: %s\n", md5sum)
// }

// // 改进的MMAP测试，使用分段处理避免一次性映射大内存
// func TestOpenFileMmapChunked(t *testing.T) {
// 	fmt.Println("=== 测试 MMAP 分段处理方式 ===")

// 	stopProfiling := startMemoryProfile("mem_mmap_chunked.prof")
// 	defer stopProfiling()

// 	file, err := os.Open(filePath)
// 	if err != nil {
// 		t.Errorf("Failed to open file: %v", err)
// 		return
// 	}
// 	defer file.Close()

// 	info, err := file.Stat()
// 	if err != nil {
// 		t.Errorf("Failed to get file info: %v", err)
// 		return
// 	}
// 	fileSize := info.Size()

// 	// 使用分段映射处理大文件
// 	hash := md5.New()
// 	chunkSize := 64 * 1024 * 1024 // 64MB 分段大小
// 	fd := int(file.Fd())

// 	for offset := int64(0); offset < fileSize; offset += int64(chunkSize) {
// 		remaining := fileSize - offset
// 		mapSize := chunkSize
// 		if remaining < int64(chunkSize) {
// 			mapSize = int(remaining)
// 		}

// 		// 映射当前分段
// 		data, err := unix.Mmap(fd, offset, mapSize, unix.PROT_READ, unix.MAP_SHARED)
// 		if err != nil {
// 			t.Errorf("MMAP failed at offset %d: %v", offset, err)
// 			return
// 		}

// 		// 更新hash
// 		hash.Write(data)

// 		// 立即取消映射
// 		if err := unix.Munmap(data); err != nil {
// 			t.Errorf("Munmap failed: %v", err)
// 			return
// 		}

// 		// 显示进度
// 		if offset%(100 * 1024 * 1024) == 0 {
// 			progress := float64(offset) / float64(fileSize) * 100
// 			fmt.Printf("处理进度: %.1f%%\n", progress)
// 		}
// 	}

// 	md5sum := fmt.Sprintf("%x", hash.Sum(nil))

// 	// 打印内存信息
// 	var mstats runtime.MemStats
// 	runtime.ReadMemStats(&mstats)
// 	fmt.Printf("文件大小: %d bytes (%.2f MB)\n", fileSize, float64(fileSize)/1024/1024)
// 	fmt.Printf("峰值内存: %.2f MB\n", float64(mstats.Alloc)/1024/1024)
// 	fmt.Printf("MD5: %s\n", md5sum)
// }

// // 同时运行三种测试进行对比
// func TestCompareMemoryUsage(t *testing.T) {
// 	fmt.Println("=== 内存使用对比测试 ===")

// 	// 测试一次性读取方式
// 	fmt.Println("1. 一次性读取方式 (os.ReadFile)")
// 	fmt.Println(strings.Repeat("-", 50))
// 	t.Run("一次性读取方式", TestReadFile)

// 	fmt.Println("\n" + strings.Repeat("=", 50) + "\n")

// 	// 测试流式处理方式
// 	fmt.Println("2. 流式处理方式 (os.Open + 缓冲区)")
// 	fmt.Println(strings.Repeat("-", 50))
// 	t.Run("流式处理方式", TestOpenFile)

// 	fmt.Println("\n" + strings.Repeat("=", 50) + "\n")

// 	// 测试MMAP方式
// 	fmt.Println("3. MMAP 内存映射方式 (一次性映射)")
// 	fmt.Println(strings.Repeat("-", 50))
// 	t.Run("MMAP方式", TestOpenFileMmap)

// 	fmt.Println("\n" + strings.Repeat("=", 50) + "\n")

// 	// 测试MMAP分段方式
// 	fmt.Println("4. MMAP 分段处理方式 (推荐用于大文件)")
// 	fmt.Println(strings.Repeat("-", 50))
// 	t.Run("MMAP分段方式", TestOpenFileMmapChunked)

// 	fmt.Println("\n" + strings.Repeat("=", 50))
// 	fmt.Println("=== 测试完成 ===")
// }

// // 生成汇总报告的函数
// func TestGenerateSummaryReport(t *testing.T) {
// 	fmt.Println("=== 内存占用汇总报告 ===")

// 	// 模拟运行并收集结果
// 	results := []struct {
// 		name        string
// 		peakMemory  float64
// 		description string
// 	}{
// 		{
// 			name:        "os.ReadFile",
// 			peakMemory:  1024.52, // 根据你的实际测试结果调整
// 			description: "一次性加载整个文件到内存",
// 		},
// 		{
// 			name:        "流式处理",
// 			peakMemory:  0.58, // 根据你的实际测试结果调整
// 			description: "固定缓冲区，内存占用稳定",
// 		},
// 		{
// 			name:        "MMAP一次性映射",
// 			peakMemory:  1024.50, // 根据实际测试调整
// 			description: "内存映射，虚拟内存占用大但物理内存按需加载",
// 		},
// 		{
// 			name:        "MMAP分段映射",
// 			peakMemory:  64.0, // 根据分段大小调整
// 			description: "分段映射，平衡性能和内存使用",
// 		},
// 	}

// 	fmt.Printf("%-20s %-12s %s\n", "方法", "峰值内存(MB)", "描述")
// 	fmt.Println(strings.Repeat("-", 60))

// 	for _, result := range results {
// 		fmt.Printf("%-20s %-12.2f %s\n", result.name, result.peakMemory, result.description)
// 	}

// 	fmt.Println("\n=== 推荐建议 ===")
// 	fmt.Println("✅ 小文件( < 100MB): 任意方式均可")
// 	fmt.Println("✅ 大文件(100MB - 1GB): 流式处理或MMAP分段")
// 	fmt.Println("✅ 超大文件( > 1GB): MMAP分段处理")
// 	fmt.Println("❌ 避免使用: os.ReadFile处理大文件")
// }

// // 性能基准测试
// // BenchmarkReadFile
// // BenchmarkReadFile-32       1        2290082822 ns/op        1073824160 B/op       63 allocs/op
// func BenchmarkReadFile(b *testing.B) {
// 	for i := 0; i < b.N; i++ {
// 		file, err := os.ReadFile(filePath)
// 		if err != nil {
// 			b.Fatal(err)
// 		}
// 		_ = calculateMD5(file)
// 	}
// }

// // BenchmarkStreaming
// // BenchmarkStreaming-32        1        2207541992 ns/op          100952 B/op         47 allocs/op
// func BenchmarkStreaming(b *testing.B) {
// 	for i := 0; i < b.N; i++ {
// 		file, err := os.Open(filePath)
// 		if err != nil {
// 			b.Fatal(err)
// 		}
// 		_, err = utils.CalculateMd5(file)
// 		file.Close()
// 		if err != nil {
// 			b.Fatal(err)
// 		}
// 	}
// }

// // BenchmarkMMAP
// // BenchmarkMMAP-32             1        2076986047 ns/op           69088 B/op         58 allocs/op
// func BenchmarkMMAP(b *testing.B) {
// 	for i := 0; i < b.N; i++ {
// 		file, err := os.Open(filePath)
// 		if err != nil {
// 			b.Fatal(err)
// 		}

// 		info, err := file.Stat()
// 		if err != nil {
// 			file.Close()
// 			b.Fatal(err)
// 		}

// 		fd := int(file.Fd())
// 		data, err := unix.Mmap(fd, 0, int(info.Size()), unix.PROT_READ, unix.MAP_SHARED)
// 		if err != nil {
// 			file.Close()
// 			b.Fatal(err)
// 		}

// 		_ = calculateMD5(data)

// 		unix.Munmap(data)
// 		file.Close()
// 	}
// }

// // goos: linux
// // goarch: amd64
// // pkg: FluteGo/test
// // cpu: AMD Ryzen 9 7940HX with Radeon Graphics
// // BenchmarkReadFile-32                   5        2217338889 ns/op        1073751556 B/op        7 allocs/op
// // BenchmarkStreaming-32                  5        2191322712 ns/op           33152 B/op         10 allocs/op
// // BenchmarkMMAP-32                       5        2074427383 ns/op             440 B/op          5 allocs/op
