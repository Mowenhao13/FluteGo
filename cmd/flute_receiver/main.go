package main

import (
	"FluteGo/constant"
	"FluteGo/pkg/system"
	"FluteGo/pkg/utils"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"
)

var (
	saveFileDir string
	destIP      string
)

func main() {
	// 创建内存profile文件
	memProfile, err := os.Create("receiver_mem_profile.pprof")
	if err != nil {
		log.Printf("Failed to create memory profile: %v", err)
		fmt.Println("按回车键退出...")
		fmt.Scanln()
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		log.Printf("Failed to write initial heap profile: %v", err)
		fmt.Println("按回车键退出...")
		fmt.Scanln()
	}

	fmt.Println("Enter dest IP, example: 192.168.1.103:3400")
	fmt.Scanln(&destIP)
	if destIP == "" {
		destIP = constant.DestIP
		fmt.Printf("Using default dest ip: %s\n", destIP)
	}

	fmt.Println("\nEnter save file dir, example: ./received_files/")
	fmt.Scanln(&saveFileDir)
	if saveFileDir == "" {
		saveFileDir = utils.SelectSaveFileDir()
	}
	fmt.Printf("Files will be saved to: %s\n", saveFileDir)

	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting Receiver System...")

	// 1. Initialize System
	// Use default max workers (0 = auto)
	sys, err := system.InitReceiverSystem(0, destIP, saveFileDir)
	if err != nil {
		log.Fatalf("Failed to initialize system: %v", err)
		fmt.Println("按回车键退出...")
		fmt.Scanln()
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
					fmt.Printf("✅ File %d transfer COMPLETED. Total: %d bytes. Progress: %d/%d",
						report.FdtID, report.TotalBytes, completedFiles, totalFiles)

					if totalFiles > 0 && completedFiles >= totalFiles {
						fmt.Println("All files received. Initiating shutdown...")
						stop() // Trigger graceful shutdown
						return
					}
				case 2: // Error
					fmt.Printf("❌ File %d transfer ERROR.\n", report.FdtID)
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
		fmt.Println("按回车键退出...")
		fmt.Scanln()
	}

	// 输出详细的内存分析结果
	fmt.Printf("=== Memory Profile Results for Current Send Session ===\n")
	fmt.Printf("Total Allocated Memory: %v bytes\n", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
	fmt.Printf("Peak Heap Memory: %v bytes, %v MB\n", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
	fmt.Printf("System Memory (Sys): %d MB\n", memStatsEnd.Sys/(1024*1024))
	fmt.Printf("Heap Idle Memory: %d MB\n", memStatsEnd.HeapIdle/(1024*1024))
	fmt.Printf("Garbage Collection Count: %v\n", memStatsEnd.NumGC-memStatsStart.NumGC)
	fmt.Printf("Memory Allocation Count: %v\n", memStatsEnd.Mallocs-memStatsStart.Mallocs)
	fmt.Printf("Heap Objects Count: %v\n", memStatsEnd.HeapObjects)
	// ctxx, cancel := context.WithCancel(context.Background())
	// defer cancel()

	// sigChan := make(chan os.Signal, 1)
	// signal.Notify(sigChan, syscall.SIGABRT, syscall.SIGALRM)

	// go func() {
	// 	<-sigChan
	// 	cancel()
	// }()

	// <-ctxx.Done()
	// fmt.Println("Exit program")
}
