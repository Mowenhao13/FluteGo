package test

import (
	"os"
	"runtime"
	"runtime/pprof"
	"testing"

	raptorq "github.com/xssnick/raptorq"
)

func TestCreateRaptorQMem(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("recv_mem_profile.pprof")
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
	runtime.ReadMemStats(&memStatsStart)  // 修正：添加这行

	for i := 0; i < 1024; i++ {
		rq := raptorq.NewRaptorQ(1024)
		if _, err := rq.CreateDecoder(1024 * 1024); err != nil {
			t.Fatalf("Failed to create RaptorQ decoder: %v", err)
		}
	}
	

	// 记录结束时的内存状态
	runtime.ReadMemStats(&memStatsEnd)  // 修正：添加这行

	// 在测试结束时获取内存快照
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 修正内存统计输出
	t.Logf("总分配内存: %v bytes", (memStatsEnd.TotalAlloc - memStatsStart.TotalAlloc))
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024 * 1024))
	t.Logf("峰值内存使用: %v MB", memStatsEnd.HeapAlloc/(1024 * 1024))
	t.Logf("堆内存分配: %v MB", memStatsEnd.HeapAlloc/(1024 * 1024))
	t.Logf("堆内存分配次数: %v", memStatsEnd.HeapAlloc)
}