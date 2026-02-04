package test

import (
	utils "FluteGo/pkg/utils"
	"os"
	"runtime"
	"runtime/pprof"
	"testing"
)



// Starting pprof server on :6060
// === RUN   TestCalMd5Mem
//     md5_test.go:45: Md5sum of file: a981130cf2b7e09f4686dc273cf7187e
//     md5_test.go:55: === 内存性能分析结果 ===
//     md5_test.go:56: 总分配内存: 34360 bytes
//     md5_test.go:57: 峰值堆内存: 1859368 bytes, 1 MB
//     md5_test.go:58: 垃圾回收次数: 0
//     md5_test.go:59: 内存分配次数: 23
//     md5_test.go:60: 堆对象数量: 1064
// --- PASS: TestCalMd5Mem (7.46s)
// PASS
// ok      FluteGo/test    7.563s


func TestCalMd5Mem(t *testing.T) {
	const (
		filePath = "/home/Halllo/Projects/Flute_test_v1/Flute_test/cmd/send_files/test_2048mb.bin"
	)

	// 创建内存profile文件
	memProfile, err := os.Create("recv_slice_mem_profile.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write initial heap profile: %v", err)
	}
	
	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)
	
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}

	md5sum, err := utils.CalculateMd5(file)
	if err != nil {
		t.Logf("Error occured: %s", err)
	}

	t.Logf("Md5sum of file: %s", md5sum)

	runtime.ReadMemStats(&memStatsEnd)
	
	// 写入最终的内存profile
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 输出详细的内存分析结果
	t.Logf("=== 内存性能分析结果 ===")
	t.Logf("总分配内存: %v bytes", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
	t.Logf("峰值堆内存: %v bytes, %v MB", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
	t.Logf("内存分配次数: %v", memStatsEnd.Mallocs-memStatsStart.Mallocs)
	t.Logf("堆对象数量: %v", memStatsEnd.HeapObjects)

}
