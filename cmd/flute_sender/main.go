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
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/schollz/progressbar/v3"
	"golang.org/x/sys/windows"
	"golang.org/x/time/rate"
)

var globalPool *pool.ConnPool
var sendFileIndex uint32

var (
	fPath              string
	otiID              uint8
	maxConcurrentSends uint8
	destIP             string
	transferringFiles  sync.Map // 用于存储当前正在传输的文件
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
	fmt.Println("Enter dest IP, example: 127.0.0.1")
	fmt.Scanln(&destIP)
	// testing
	if destIP == "" {
		destIP = constant.DestIP
		fmt.Printf("Using default dest ip: %s\n", destIP)
	}

	fmt.Println("\nEnter directory containing files or single file path to send, example: cmd/send_files")
	fmt.Scanln(&fPath)
	if fPath == "" {
		fPath = utils.SelectSendFileDir()
		log.Printf("Using default send file directory: %s", fPath)
	}

	info, err := os.Stat(fPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("Path does not exist: %s", fPath)
			fmt.Println("按回车键退出...")
			fmt.Scanln()
			return
		} else {
			log.Printf("Failed to access path: %v", err)
			fmt.Println("按回车键退出...")
			fmt.Scanln()
			return
		}
	}

	var sendFileList []*os.File

	if info.IsDir() {
		log.Printf("Sending files from directory: %s", fPath)

		files, err := os.ReadDir(fPath)
		if err != nil {
			log.Printf("Failed to read directory: %v", err)
			fmt.Println("按回车键退出...")
			fmt.Scanln()
		}

		utils.ListDir(fPath)

		for _, file := range files {
			if !file.IsDir() {
				f, err := os.Open(fPath + file.Name())
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
			log.Printf("No files found in %s", fPath)
			fmt.Println("按回车键退出...")
			fmt.Scanln()
			return
		}

	} else {
		log.Printf("Sending single file: %s", fPath)
		f, err := os.Open(fPath)
		if err != nil {
			log.Printf("Failed to open file: %v", err)
			fmt.Println("按回车键退出...")
			fmt.Scanln()
		}
		sendFileList = append(sendFileList, f)
	}

	fmt.Println("Enter OTI ID (0: NoCode, 1: RaptorQ, 2: Reed-Solomon), default is 0")
	fmt.Scanln(&otiID)
	if otiID > 2 {
		otiID = 0
		log.Printf("Invalid OTI ID %d, defaulting to No-code", otiID)
	}

	fmt.Println("Enter max concurrent sends (default 1)")
	fmt.Scanln(&maxConcurrentSends)
	if maxConcurrentSends == 0 {
		maxConcurrentSends = 1
		log.Printf("Invalid max concurrent sends %d, defaulting to 1", maxConcurrentSends)
	}

	fdtID := uint8(1)

	var o oti.Oti

	switch otiID {
	case 0:
		o = oti.NewNoCode(1400)
		log.Printf("Using OTI: NoCode")
	case 1:
		o = oti.NewRaptorQ(1400)
		log.Printf("Using OTI: RaptorQ")
	case 2:
		o = oti.NewReedSolomon(12, 4)
		log.Printf("Using OTI: Reed-Solomon")
	default:
		log.Printf("Invalid OTI ID %d, defaulting to Reed-Solomon", otiID)
	}

	pool.InitConnPool(destIP, 0)
	globalPool = pool.GetConnPool()
	if globalPool == nil {
		log.Printf("Pool not initialized\n")
		fmt.Println("按回车键退出...")
		fmt.Scanln()
		return
	}

	_, err = globalPool.InitMetaConn()
	if err != nil {
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

	// Create global rate limiter
	limiter, _ := sender.CreateRateLimiter(constant.DefaultSendRateLimitMbps, 1500)

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	totalFiles := len(sendFileList)
	currentBasePort := constant.BASE_FILE_PORT

	for i, file := range sendFileList {
		sem <- struct{}{}
		currFdtID := fdtID
		fdtID++

		fileBasePort := currentBasePort
		currentBasePort += constant.NUM_PORTS

		wg.Add(1)

		go func(idx int, f *os.File, fid uint8, portBase int) {
			defer wg.Done()
			defer func() { <-sem }()

			numPorts := uint8(constant.NUM_PORTS)
			conns, connErrs := globalPool.CreateFileConn(fid, numPorts, portBase)
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

			// Use the intended destination port as basePort for metadata
			basePort := portBase
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

			// 记录正在传输的文件
			transferringFiles.Store(fid, metaPkt.File.Name)
			defer transferringFiles.Delete(fid)

			// Create progress bar
			bar := progressbar.NewOptions64(
				int64(metaPkt.File.TransferLen),
				progressbar.OptionSetDescription(fmt.Sprintf("[%s]", metaPkt.File.Name)),
				progressbar.OptionSetWriter(os.Stderr),
				progressbar.OptionShowBytes(true),
				progressbar.OptionSetWidth(15),
				progressbar.OptionThrottle(65*time.Millisecond),
				progressbar.OptionShowCount(),
				progressbar.OptionOnCompletion(func() {
					fmt.Fprint(os.Stderr, "\n")
				}),
				progressbar.OptionSpinnerType(14),
				progressbar.OptionFullWidth(),
			)

			metaPkt.ShowPktInfo()
			if err := SendFile(metaPkt, limiter, bar); err != nil {
				log.Printf("Failed to send file(fdtID: %d): %v", fid, err)
				bar.Finish()
				fmt.Println("按回车键退出...")
				fmt.Scanln()
				return
			}
			bar.Finish()
		}(i, file, currFdtID, fileBasePort)

	}

	wg.Wait()

	time.Sleep(500 * time.Millisecond)
	fmt.Printf("All files have been processed.\n")

	runtime.ReadMemStats(&memStatsEnd)

	// 写入最终的内存profile
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		log.Printf("Failed to write final heap profile: %v", err)
		fmt.Println("按回车键退出...")
		fmt.Scanln()
	}

	// 输出详细的内存分析结果
	fmt.Printf("=== Memory Profile Results for Current Receive Session ===\n")
	fmt.Printf("Total Allocated Memory: %v bytes\n", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
	fmt.Printf("Peak Heap Memory: %v bytes, %v MB\n", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
	fmt.Printf("System Memory (Sys): %d MB\n", memStatsEnd.Sys/(1024*1024))
	fmt.Printf("Heap Idle Memory: %d MB\n", memStatsEnd.HeapIdle/(1024*1024))
	fmt.Printf("Garbage Collection Count: %v\n", memStatsEnd.NumGC-memStatsStart.NumGC)
	fmt.Printf("Memory Allocation Count: %v\n", memStatsEnd.Mallocs-memStatsStart.Mallocs)
	fmt.Printf("Heap Objects Count: %v\n", memStatsEnd.HeapObjects)

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

func sendData(wsck *pool.WinSocket, data []byte) error {
	var wsaBuf windows.WSABuf
	var byteSent uint32
	wsaBuf.Len = uint32(len(data))
	wsaBuf.Buf = &data[0]
	err := windows.WSASendTo(wsck.Socket, &wsaBuf, 1, &byteSent, wsck.Flags, wsck.To.ToAny, wsck.To.ToLen, nil, nil)
	if err == nil {
		wsck.MarkSent()
	}
	return err
}

func SendFile(mt *meta.MetaPkt, limiter *rate.Limiter, bar *progressbar.ProgressBar) error {
	metaConn, err := globalPool.GetMetaConn()
	if err != nil {
		return err
	}

	metaData := mt.Serialize()

	log.Printf("[SendFile] Meta connection: %s (Mode: %d, FdtID: %d)",
		metaConn.Addr, globalPool.Mode, metaConn.FdtID)
	log.Printf("[SendFile] Sending metadata: %d bytes to %s", len(metaData), metaConn.Addr)

	if err := sendData(metaConn, metaData); err != nil {
		log.Printf("[SendFile] Failed to send metadata: %v", err)
		return err
	}

	log.Printf("[SendFile] Metadata sent successfully")

	log.Printf("Sender will be started after %d seconds\n", constant.START_SEND_WAIT)
	time.Sleep(constant.START_SEND_WAIT * time.Second)

	sender, err := sender.InitSender(mt, limiter)
	if err != nil {
		return fmt.Errorf("Failed to init sender: %v", err)
	}

	if bar != nil {
		// Update bar total with actual bytes to send (including overhead)
		totalBytes := sender.GetTotalBytesToSend()
		bar.ChangeMax64(totalBytes)
		sender.SetProgressCallback(func(sent int64) {
			bar.Set64(sent)
		})
	}

	err = sender.Start(context.Background())
	if err == nil && bar != nil {
		bar.Finish()
	}
	return err
}
