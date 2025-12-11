package main

import (
	"FluteGo/constant"
	"FluteGo/pkg/meta"
	"FluteGo/pkg/oti"
	"FluteGo/pkg/pool"
	sender "FluteGo/pkg/sender"
	"FluteGo/pkg/utils"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var globalPool *pool.GlobalConnectionPool
var sendFileIndex uint32

var (
	sendFileDir        string
	otiID              uint8
	maxConcurrentSends uint8
	destIP             string
	destMac     string 
)

func main() {
	// 创建内存profile文件
	memProfile, err := os.Create("sender_mem_profile.pprof")
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

	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	// 解析Input参数
	fmt.Println("Enter dest IP, example: 192.168.1.103:3400")
	fmt.Scanln(&destIP)
	// testing
	if destIP == "" {
		destIP = constant.DestIP
		fmt.Printf("Using default dest ip: %s\n", destIP)
	}

	fmt.Println("Enter dest mac, example: 00:11:22:33:44:55")
	fmt.Scanln(&destMac)
	if destMac == "" {
		fmt.Println("获取接收方MAC地址失败")
		fmt.Println("按回车键退出...")
		fmt.Scanln()
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		fmt.Printf("获取可执行文件路径失败: %v\n", err)
		fmt.Println("按回车键退出...")
		fmt.Scanln()
		return
	}

	fmt.Println("\nEnter directory containing files to send, example: cmd/send_files")
	fmt.Printf("Current base file: %s\n", exePath)
	fmt.Scanln(&sendFileDir)
	if sendFileDir == "" {
		sendFileDir = utils.SelectSendFileDir()
		log.Printf("Using default send file directory: %s", sendFileDir)
	}

	fmt.Println("Enter OTI ID (0: NoCode, 1: RaptorQ, 2: Reed-Solomon), default is 2")
	fmt.Scanln(&otiID)
	if otiID < 0 || otiID > 2 {
		otiID = 2
		log.Printf("Invalid OTI ID %d, defaulting to Reed-Solomon", otiID)
	}

	fmt.Println("Enter max concurrent sends (default 1)")
	fmt.Scanln(&maxConcurrentSends)
	if maxConcurrentSends == 0 {
		maxConcurrentSends = 1
		log.Printf("Invalid max concurrent sends %d, defaulting to 1", maxConcurrentSends)
	}

	if err := utils.SetNetLink(destIP, destMac); err != nil {
		fmt.Printf("无法建立单向连接: %v", err)
		fmt.Println("按回车键退出...")
		fmt.Scanln()
	}

	files, err := os.ReadDir(sendFileDir)
	if err != nil {
		log.Printf("Failed to read directory: %v", err)
		fmt.Println("按回车键退出...")
		fmt.Scanln()
	}

	fdtID := uint8(1)
	var sendFileList []*os.File
	for _, file := range files {
		if !file.IsDir() {
			f, err := os.Open(sendFileDir + file.Name())
			if err != nil {
				log.Printf("Failed to open file: %v", err)
				fmt.Println("按回车键退出...")
				fmt.Scanln()
			}
			defer f.Close()
			sendFileList = append(sendFileList, f)
		}
	}

	if len(sendFileList) == 0 {
		log.Printf("No files found in %s", sendFileDir)
		fmt.Println("按回车键退出...")
		fmt.Scanln()
		return
	}

	var o oti.Oti

	switch otiID {
	case 0:
		o = oti.NewNoCode(1400)
		log.Printf("Using OTI: NoCode")
	case 1:
		o = oti.NewRaptorQ(1400)
		log.Printf("Using OTI: RaptorQ (Not implemented, defaulting to Reed-Solomon)")
	case 2:
		o = oti.NewReedSolomon(12, 4)
		log.Printf("Using OTI: Reed-Solomon")
	default:
		log.Printf("Invalid OTI ID %d, defaulting to Reed-Solomon", otiID)
	}

	pool.InitGlobalConnectionPool(100, constant.MaxMetaConnTimeout, 0, destIP)
	globalPool = pool.GetGlobalPool()
	if globalPool == nil {
		log.Printf("Pool not initialized\n")
		fmt.Println("按回车键退出...")
		fmt.Scanln()
		return
	}

	_, errs := globalPool.InitMetaConn()
	if len(errs) > 0 {
		log.Printf("Failed to create MetaPkt connection\n")
		fmt.Println("按回车键退出...")
		fmt.Scanln()
		return
	}

	defer globalPool.CloseMetaConn()

	maxConcurrent := maxConcurrentSends
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	totalFiles := len(sendFileList)
	for i, file := range sendFileList {
		sem <- struct{}{}
		currFdtID := fdtID
		fdtID++
		wg.Add(1)

		go func(idx int, f *os.File, fid uint8) {
			defer wg.Done()
			defer func() { <-sem }()

			numPorts := uint8(constant.NumPorts)
			conns, connErrs := globalPool.CreateNewFileConn(fid, numPorts)
			if len(connErrs) > 0 {
				for _, cErr := range connErrs {
					if cErr != nil {
						log.Printf("Failed to create data connection for fdtID %d: %v", fid, cErr)
					}
				}
			}
			if len(conns) == 0 {
				log.Printf("No data connections available for fdtID %d, skip file", fid)
				fmt.Println("按回车键退出...")
				fmt.Scanln()
				return
			}
			defer globalPool.CloseFileConn(fid)

			basePort := portFromConn(conns[0].Conn)
			metaPkt, err := meta.InitMetaPkt(f, o, basePort, uint16(numPorts), fid)
			if err != nil {
				log.Printf("Failed to init MetaPkt: %v", err)
				fmt.Println("按回车键退出...")
				fmt.Scanln()
				return
			}
			metaPkt.TotalFiles = uint16(totalFiles)
			metaPkt.CurrentFileIndex = uint16(atomic.AddUint32(&sendFileIndex, 1))
			log.Printf("Initialized MetaPkt for file: %s, FdtID: %d", metaPkt.File.Name, metaPkt.File.FdtID)

			metaPkt.ShowPktInfo()
			if err := SendFile(metaPkt); err != nil {
				log.Printf("Failed to send file(fdtID: %d): %v", fid, err)
				fmt.Println("按回车键退出...")
				fmt.Scanln()
				return
			}
		}(i, file, currFdtID)

	}
	wg.Wait()

	log.Printf("All files have been processed.\n")

	runtime.ReadMemStats(&memStatsEnd)

	// 写入最终的内存profile
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		log.Printf("Failed to write final heap profile: %v", err)
		fmt.Println("按回车键退出...")
		fmt.Scanln()
	}

	// 输出详细的内存分析结果
	log.Printf("=== 本次发送会话内存性能分析结果 ===")
	log.Printf("总分配内存: %v bytes", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
	log.Printf("峰值堆内存: %v bytes, %v MB", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
	log.Printf("系统申请内存 (Sys): %d MB", memStatsEnd.Sys/(1024*1024))
	log.Printf("堆空闲内存 (HeapIdle): %d MB", memStatsEnd.HeapIdle/(1024*1024))
	log.Printf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
	log.Printf("内存分配次数: %v", memStatsEnd.Mallocs-memStatsStart.Mallocs)
	log.Printf("堆对象数量: %v", memStatsEnd.HeapObjects)

	ctxx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGABRT, syscall.SIGALRM)

	go func() {
		<-sigChan
		cancel()
	}()

	<-ctxx.Done()
	fmt.Println("Exit program")
}

func portFromConn(conn *net.UDPConn) int {
	addr := conn.RemoteAddr()
	if udpAddr, ok := addr.(*net.UDPAddr); ok {
		return udpAddr.Port
	}
	return constant.BaseFilePort
}

func SendFile(mt *meta.MetaPkt) error {
	metaConn, err := globalPool.GetMetaConn()
	if err != nil {
		return err
	}

	metaData := mt.Serialize()

	log.Printf("[SendFile] Meta connection: %s (Mode: %d, FdtID: %d)",
		metaConn.Conn.RemoteAddr(), metaConn.Mode, metaConn.FdtID)
	log.Printf("[SendFile] Sending metadata: %d bytes to %s", len(metaData), metaConn.Conn.RemoteAddr())

	if _, err := metaConn.Conn.Write(metaData); err != nil {
		log.Printf("[SendFile] Failed to send metadata: %v", err)
		return err
	}

	log.Printf("[SendFile] Metadata sent successfully")

	log.Printf("Sender will be started after %d seconds\n", constant.StartSendWait)
	time.Sleep(constant.StartSendWait * time.Second)

	sender, err := sender.InitSender(mt)
	if err != nil {
		return fmt.Errorf("Failed to init sender: %v", err)
	}

	return sender.Start(context.Background())
}
