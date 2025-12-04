package main

import (
	"FluteGo/constant"
	meta "FluteGo/pkg/meta"
	"FluteGo/pkg/oti"
	"FluteGo/pkg/pool"
	sender "FluteGo/pkg/sender"
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"testing"
	"time"
)

var sendPool *pool.GlobalConnectionPool

const (
	test_1024mb = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1024mb.bin"
	test_500mb  = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_500mb.bin"
	test_100mb  = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_100mb.bin"
)

func BenchmarkSender(b *testing.B) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("\n\n❌ 程序发生 panic: %v\n", r)
			log.Printf("请检查错误并重新启动\n")
		}
	}()

	// 创建内存profile文件
	memProfile, err := os.Create("sender_mem_profile.pprof")
	if err != nil {
		log.Printf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		log.Printf("Failed to write initial heap profile: %v", err)
	}

	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	file, err := os.Open(test_1024mb)
	if err != nil {
		log.Printf("Failed to open file: %v", err)
	}
	defer file.Close()

	fdtID := uint8(1)
	oti := oti.NewRaptorQ(1400)
	if oti.MaximumChunkSize == 0 {
		oti.MaximumChunkSize = uint32(constant.DefaultChunkSize)
	}

	// Align ChunkSize to page size to ensure consistency between Sender (mmap) and Receiver
	pageSize := os.Getpagesize()
	if int(oti.MaximumChunkSize)%pageSize != 0 {
		alignedSize := uint32(((int(oti.MaximumChunkSize) + pageSize - 1) / pageSize) * pageSize)
		log.Printf("Aligning ChunkSize from %d to %d (PageSize: %d)", oti.MaximumChunkSize, alignedSize, pageSize)
		oti.MaximumChunkSize = alignedSize
	}

	pool.InitGlobalConnectionPool(100, constant.MaxMetaConnTimeout, 0)
	sendPool = pool.GetGlobalPool()
	if sendPool == nil {
		log.Panic("Pool not initialized\n")
	}
	defer sendPool.Stop()

	_, errs := sendPool.InitMetaConn()
	if len(errs) > 0 {
		log.Panic("Failed to create MetaPkt connection\n")
	}

	defer sendPool.CloseMetaConn()
	maxConcurrent := constant.MaxConcurrentSends
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	wg.Add(1)

	defer wg.Done()
	defer func() { <-sem }()

	numPorts := uint8(constant.NumPorts)
	conns, connErrs := sendPool.CreateNewFileConn(fdtID, numPorts)
	if len(connErrs) > 0 {
		for _, cErr := range connErrs {
			if cErr != nil {
				log.Printf("Failed to create data connection for fdtID %d: %v", fdtID, cErr)
			}
		}
	}
	if len(conns) == 0 {
		log.Printf("No data connections available for fdtID %d, skip file", fdtID)
		return
	}
	defer sendPool.CloseFileConn(fdtID)

	basePort := 3400
	metaPkt, err := meta.InitMetaPkt(file, oti, basePort, uint16(numPorts), fdtID, constant.SaveFileDir)
	if err != nil {
		log.Printf("Failed to init MetaPkt: %v", err)
		return
	}
	metaPkt.TotalFiles = 1
	metaPkt.CurrentFileIndex = 1
	metaPkt.ShowPktInfo()
	if err := sendFile(metaPkt); err != nil {
		log.Printf("Failed to send file(fdtID: %d): %v", fdtID, err)
	}
	wg.Wait()

	log.Printf("All files have been processed.\n")

	runtime.ReadMemStats(&memStatsEnd)

	// 写入最终的内存profile
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		log.Printf("Failed to write final heap profile: %v", err)
	}

	// 输出详细的内存分析结果
	log.Printf("=== 内存性能分析结果 ===")
	log.Printf("总分配内存: %v bytes", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
	log.Printf("峰值堆内存: %v bytes, %v MB", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
	log.Printf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
	log.Printf("内存分配次数: %v", memStatsEnd.Mallocs-memStatsStart.Mallocs)
	log.Printf("堆对象数量: %v", memStatsEnd.HeapObjects)
	log.Printf("BenchmarkSender finished successfully")
}

func sendFile(mt *meta.MetaPkt) error {
	metaConn, err := sendPool.GetMetaConn()
	if err != nil {
		return err
	}

	metaData := mt.Serialize()

	if _, err := metaConn.Conn.Write(metaData); err != nil {
		return err
	}

	log.Printf("Sender will be started after 3 seconds\n")
	time.Sleep(3 * time.Second)

	sender, err := sender.InitSender(mt)
	if err != nil {
		return fmt.Errorf("Failed to init sender: %v", err)
	}

	return sender.Start(context.Background())
}
