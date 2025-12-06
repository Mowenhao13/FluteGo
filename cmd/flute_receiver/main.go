package main

import (
	"FluteGo/constant"
	"FluteGo/pkg/system"
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"
)

var (
	saveFileDir = flag.String("recvdir", constant.SaveFileDir, "Directory to receive files")
	destIP      = flag.String("dest", constant.DestIP, "Destination IP address")
)

func main() {
	// 创建内存profile文件
	memProfile, err := os.Create("receiver_mem_profile.pprof")
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
	
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting Receiver System...")

	// 1. Initialize System
	// Use default max workers (0 = auto)
	sys, err := system.InitReceiverSystem(0, *destIP, *saveFileDir)
	if err != nil {
		log.Fatalf("Failed to initialize system: %v", err)
	}

	// Handle OS signals for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 2. Start Error Handling
	sys.StartErrorProgram()
	log.Println("Error handling subsystem started.")

	// 3. Start Meta Receiver
	sys.StartMetaProgram()
	log.Println("Meta receiver subsystem started.")

	// 4. Start File Receiver Workers
	sys.StartFileProgram()
	log.Println("File receiver subsystem started.")

	// 5. Monitor Progress
	go func() {
		log.Println("Monitoring file progress...")
		completedFiles := 0
		totalFiles := -1

		for {
			select {
			case <-ctx.Done():
				return
			case report := <-sys.FileReporter.ReportChan:
				switch report.Status {
				case 0: // Transferring
					if totalFiles == -1 && report.TotalFiles > 0 {
						totalFiles = int(report.TotalFiles)
						log.Printf("Session total files: %d", totalFiles)
					}
				case 1: // Completed
					completedFiles++
					log.Printf("✅ File %d transfer COMPLETED. Total: %d bytes. Progress: %d/%d",
						report.FdtID, report.TotalBytes, completedFiles, totalFiles)

					if totalFiles > 0 && completedFiles >= totalFiles {
						log.Println("All files received. Initiating shutdown...")
						stop() // Trigger graceful shutdown
						return
					}
				case 2: // Error
					log.Printf("❌ File %d transfer ERROR.", report.FdtID)
				}
			}
		}
	}()

	// Wait for signal
	<-ctx.Done()
	log.Println("Shutdown signal received. Cleaning up...")

	// Give some time for cleanup if needed, though system context cancellation should handle it
	time.Sleep(1 * time.Second)
	log.Println("System shutdown complete.")

	runtime.ReadMemStats(&memStatsEnd)

	// 写入最终的内存profile
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		log.Printf("Failed to write final heap profile: %v", err)
	}

	// 输出详细的内存分析结果
	log.Printf("=== 内存性能分析结果 ===")
	log.Printf("总分配内存: %v bytes", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
	log.Printf("峰值堆内存: %v bytes, %v MB", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
	log.Printf("系统申请内存 (Sys): %d MB", memStatsEnd.Sys/(1024*1024))
	log.Printf("堆空闲内存 (HeapIdle): %d MB", memStatsEnd.HeapIdle/(1024*1024))
	log.Printf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
	log.Printf("内存分配次数: %v", memStatsEnd.Mallocs-memStatsStart.Mallocs)
	log.Printf("堆对象数量: %v", memStatsEnd.HeapObjects)
}
