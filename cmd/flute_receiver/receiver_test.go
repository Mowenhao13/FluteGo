package main

import (
	"FluteGo/constant"
	"FluteGo/pkg/meta"
	"FluteGo/pkg/pool"
	"FluteGo/pkg/receiver"
	"context"
	"log"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"testing"
	"time"
)

// ReceiveFile: test_100mb.bin
// Fec: RaptorQ
// Fec Param: SymbolSize(1400)
// Conns: 1
// ReadLoop:
//	workerCount: 1
// RecvRedundancyRatio: 1.01
// DefaultChunkSize: 1 * 1024 * 1024 (1MB chunk)

var recvPool *pool.GlobalConnectionPool

func BenchmarkReceiver(b *testing.B) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("\n\n❌ 程序发生 panic: %v\n", r)
			log.Printf("请检查错误并重新启动\n")
		}
	}()

	// 创建内存profile文件
	memProfile, err := os.Create("recv_singleFile_v1_mem_profile.pprof")
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

	pool.InitGlobalConnectionPool(100, constant.MaxMetaConnTimeout, 1)
	recvPool = pool.GetGlobalPool()
	if recvPool == nil {
		log.Panic("Pool not initialized\n")
	}

	metaConns, errs := recvPool.InitMetaConn()
	if len(metaConns) == 0 {
		log.Panic("Failed to initialize meta connections\n")
	}
	if len(errs) > 0 {
		for _, err := range errs {
			if err != nil {
				log.Panicf("Failed to create MetaPkt connection: %v\n", err)
			}
		}
	}

	metaConn := metaConns[0]
	var wg sync.WaitGroup

	completeChan := make(chan struct{}, 1)

	for {
		n, _, err := metaConn.Conn.ReadFromUDP(metaConn.Buffer)
		if err != nil {
			continue
		}

		data := metaConn.Buffer[:n]

		mt, merr := meta.DeserializeMetaPkt(data)
		if merr != nil {
			log.Printf("Failed to deserialize meta packet: %v\n", merr)
			continue
		}
		mt.ShowPktInfo()

		conns, _ := recvPool.CreateNewFileConnWithBasePort(mt.File.FdtID, uint8(mt.NumPorts), mt.BasePort)
		if len(conns) == 0 {
			log.Printf("Failed to create any connections for fdtID %d", mt.File.FdtID)
			// Instead of panic, we can skip this file or retry
			// panic(fmt.Errorf("Failed to create any connections for fdtID %d", mt.File.FdtID))
			continue
		}

		wg.Add(1)
		go func(task *meta.MetaPkt) {
			defer wg.Done()
			defer recvPool.CloseFileConn(task.File.FdtID)

			recv, err := receiver.InitReceiver(task)
			if err != nil {
				log.Printf("Failed to initialize receiver: %v\n", err)
				return
			}

			recv.OnComplete = func() {
				select {
				case completeChan <- struct{}{}:
				default: // 阻止阻塞
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			if err := recv.Start(ctx); err != nil {
				log.Printf("文件接收失败 (fdtID=%d): %v", task.File.FdtID, err)
			}
		}(mt)

		break
	}

	select {
	case <-completeChan:
		b.Logf("File transfer completed")
	case <-time.After(5 * time.Second):
		b.Logf("Exceed time limit, exit program")
	}

	wg.Wait()
	runtime.ReadMemStats(&memStatsEnd)
	if _, err := memProfile.Seek(0, 0); err == nil {
		if err := memProfile.Truncate(0); err != nil {
			log.Printf("Failed to truncate memory profile file: %v", err)
		} else if err := pprof.WriteHeapProfile(memProfile); err != nil {
			log.Printf("Failed to write final heap profile: %v", err)
		}
	} else {
		log.Printf("Failed to reset memory profile file for final snapshot: %v", err)
	}

	log.Printf("=== 接收端内存性能分析结果 ===")
	log.Printf("总分配内存 (TotalAlloc): %d MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024*1024))
	log.Printf("峰值堆内存 (HeapAlloc): %d MB", memStatsEnd.HeapAlloc/(1024*1024))
	log.Printf("系统申请内存 (Sys): %d MB", memStatsEnd.Sys/(1024*1024))
	log.Printf("堆空闲内存 (HeapIdle): %d MB", memStatsEnd.HeapIdle/(1024*1024))
	log.Printf("垃圾回收次数: %d", memStatsEnd.NumGC-memStatsStart.NumGC)
}
