package test

// import (
// 	"fmt"
// 	"io"
// 	"os"
// 	"runtime"
// 	"runtime/pprof"
// 	"testing"
// )

// const (
// 	src = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1024mb.bin"
// 	dst = "/home/Halllo/Projects/Flute_test_v2/cmd/work_files/test_1024mb_work.bin"
// )

// func copyFile(src, dst string) (int64, error) {
// 	sourceFileStat, err := os.Stat(src)
// 	if err != nil {
// 		return 0, err
// 	}
// 	if !sourceFileStat.Mode().IsRegular() {
// 		return 0, fmt.Errorf("%s is not a regular file", src)
// 	}

// 	source, err := os.Open(src)
// 	if err != nil {
// 		return 0, err
// 	}
// 	defer source.Close()

// 	destination, err := os.Create(dst)
// 	if err != nil {
// 		return 0, err
// 	}
// 	defer destination.Close()

// 	nBytes, err := io.Copy(destination, source)
// 	return nBytes, err
// }

// // Copy 1024mb file test result
// // Starting pprof server on :6060
// // === RUN   TestCopy
// //     copy_test.go:73: === 内存性能分析结果 ===
// //     copy_test.go:74: 总分配内存: 704 bytes
// //     copy_test.go:75: 峰值堆内存: 1819488 bytes, 1 MB
// //     copy_test.go:76: 垃圾回收次数: 0
// //     copy_test.go:77: 内存分配次数: 12
// //     copy_test.go:78: 堆对象数量: 1027
// // --- PASS: TestCopy (1.49s)
// // PASS
// // ok      FluteGo/test    1.596s


// func TestCopy(t *testing.T) {
// 	// 创建内存profile文件
// 	memProfile, err := os.Create("copy_file_mem_profile.pprof")
// 	if err != nil {
// 		t.Fatalf("Failed to create memory profile: %v", err)
// 	}
// 	defer memProfile.Close()

// 	// 在测试开始时获取内存快照
// 	runtime.GC()
// 	if err := pprof.WriteHeapProfile(memProfile); err != nil {
// 		t.Fatalf("Failed to write initial heap profile: %v", err)
// 	}
	
// 	// 记录开始时的内存状态
// 	var memStatsStart, memStatsEnd runtime.MemStats
// 	runtime.ReadMemStats(&memStatsStart)

// 	if _, err := copyFile(src, dst); err != nil {
// 		panic(err)
// 	}

// 	// 重要：在操作后读取结束时的内存状态
// 	runtime.ReadMemStats(&memStatsEnd)
	
// 	// 写入最终的内存profile
// 	if err := pprof.WriteHeapProfile(memProfile); err != nil {
// 		t.Fatalf("Failed to write final heap profile: %v", err)
// 	}

// 	// 输出详细的内存分析结果
// 	t.Logf("=== 内存性能分析结果 ===")
// 	t.Logf("总分配内存: %v bytes", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
// 	t.Logf("峰值堆内存: %v bytes, %v MB", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
// 	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
// 	t.Logf("内存分配次数: %v", memStatsEnd.Mallocs-memStatsStart.Mallocs)
// 	t.Logf("堆对象数量: %v", memStatsEnd.HeapObjects)
// }

