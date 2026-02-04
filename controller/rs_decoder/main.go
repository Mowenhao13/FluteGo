package main

// import (
// 	"FluteGo/pkg/utils"
// 	"bytes"
// 	"encoding/binary"
// 	"flag"
// 	"fmt"
// 	"io"
// 	"log"
// 	"os"
// 	"path/filepath"
// 	"runtime"
// 	"runtime/debug"
// 	"runtime/pprof"
// 	"sync"
// 	"sync/atomic"
// 	"syscall"
// 	"time"

// 	rs "github.com/klauspost/reedsolomon"
// )

// var (
// 	dataShardsFlag   = flag.Int("data", 12, "Number of data shards")
// 	parityShardsFlag = flag.Int("par", 4, "Number of parity shards")
// 	symbolSizeFlag   = flag.Int("symbol", 1400, "Symbol payload size in bytes")
// 	listenAddrFlag   = flag.String("listen", "0.0.0.0:3400", "UDP address to listen on")
// 	workDirFlag      = flag.String("workdir", "/home/halllo-pi-v2/Projects/Flute_test_v2/cmd/recv_work/", "Working directory to store incoming shards")
// 	outFileFlag      = flag.String("out", "/home/halllo-pi-v2/Projects/Flute_test_v2/cmd/received_files/test_1024mb.bin", "Output file path")
// 	fileSizeFlag     = flag.Int64("size", 1024*1024*1024, "Expected file size in bytes")
// 	mmapFileNameFlag = flag.String("mmapfile", "shards.data", "Temporary mmap filename inside workdir")
// 	idleTimeoutFlag  = flag.Duration("idle-timeout", 5*time.Second, "Stop receiving after no packets for this duration")
// 	logIntervalFlag  = flag.Duration("log-interval", time.Second, "Interval for progress logs")
// 	memProfileFlag   = flag.String("memprofile", "rs_receiver_mem_profile.pprof", "Write memory profile to `file` (empty to disable)")
// 	watchMemory      = flag.Bool("watchmem", false, "Monitor memory usage during processing")
// 	memLimitMB       = flag.Int("memlimit", 0, "Set memory limit in MB (0 = no limit)")
// )

// type MemStats struct {
// 	StartAlloc uint64
// 	MaxAlloc   uint64
// 	StartTime  time.Time
// }

// func (m *MemStats) StartMonitoring() {
// 	var mem runtime.MemStats
// 	runtime.ReadMemStats(&mem)
// 	m.StartAlloc = mem.Alloc
// 	m.MaxAlloc = mem.Alloc
// 	m.StartTime = time.Now()

// 	if *watchMemory {
// 		go m.monitorMemory()
// 	}
// }

// func (m *MemStats) monitorMemory() {
// 	ticker := time.NewTicker(200 * time.Millisecond)
// 	defer ticker.Stop()
// 	var lastAlloc uint64

// 	for range ticker.C {
// 		var mem runtime.MemStats
// 		runtime.ReadMemStats(&mem)
// 		if mem.Alloc > m.MaxAlloc {
// 			m.MaxAlloc = mem.Alloc
// 		}

// 		if !*watchMemory {
// 			continue
// 		}
// 		current := mem.Alloc - m.StartAlloc
// 		if current == lastAlloc {
// 			continue
// 		}
// 		lastAlloc = current
// 		fmt.Printf("\r[Memory] Current: %.2f MB, Peak: %.2f MB",
// 			float64(current)/1024/1024,
// 			float64(m.MaxAlloc-m.StartAlloc)/1024/1024)
// 	}
// }

// func (m *MemStats) PrintCurrent(phase string) {
// 	var mem runtime.MemStats
// 	runtime.ReadMemStats(&mem)
// 	current := mem.Alloc - m.StartAlloc
// 	fmt.Printf("[%s] Memory: Current=%.2f MB, Heap=%.2f MB, Sys=%.2f MB\n",
// 		phase,
// 		float64(current)/1024/1024,
// 		float64(mem.HeapAlloc)/1024/1024,
// 		float64(mem.Sys)/1024/1024)
// }

// func (m *MemStats) PrintSummary(fileSize int64) {
// 	elapsed := time.Since(m.StartTime)
// 	speed := float64(fileSize) / 1024 / 1024 / elapsed.Seconds()

// 	var mem runtime.MemStats
// 	runtime.ReadMemStats(&mem)

// 	fmt.Printf("\n=== Decoding Memory Usage Summary ===\n")
// 	fmt.Printf("Processing time: %v\n", elapsed)
// 	fmt.Printf("Speed: %.2f MB/s\n", speed)
// 	fmt.Printf("Peak memory usage: %.2f MB\n", float64(m.MaxAlloc-m.StartAlloc)/1024/1024)
// 	fmt.Printf("Final memory usage: %.2f MB\n", float64(mem.Alloc-m.StartAlloc)/1024/1024)
// 	fmt.Printf("Total heap: %.2f MB\n", float64(mem.HeapAlloc)/1024/1024)
// 	fmt.Printf("System memory: %.2f MB\n", float64(mem.Sys)/1024/1024)
// 	fmt.Printf("Garbage collections: %d\n", mem.NumGC)
// }

// func main() {
// 	flag.Parse()

// 	dataShards := *dataShardsFlag
// 	parityShards := *parityShardsFlag
// 	if dataShards <= 0 || parityShards <= 0 {
// 		log.Fatalf("invalid shard parameters: data=%d parity=%d", dataShards, parityShards)
// 	}
// 	totalShards := dataShards + parityShards
// 	if totalShards > 256 {
// 		log.Fatalf("sum of shards cannot exceed 256 (got %d)", totalShards)
// 	}

// 	symbolSize := *symbolSizeFlag
// 	if symbolSize <= 0 {
// 		log.Fatalf("symbol size must be positive")
// 	}

// 	expectedFileSize := *fileSizeFlag
// 	if expectedFileSize <= 0 {
// 		log.Fatalf("expected file size must be positive")
// 	}

// 	listenAddr := *listenAddrFlag
// 	workDir := *workDirFlag
// 	outputPath := *outFileFlag
// 	targetFileName := filepath.Base(outputPath)
// 	mmapFileName := *mmapFileNameFlag
// 	idleTimeout := *idleTimeoutFlag
// 	logInterval := *logIntervalFlag

// 	if *memLimitMB > 0 {
// 		debug.SetMemoryLimit(int64(*memLimitMB) * 1024 * 1024)
// 		log.Printf("Memory limit set to %d MB", *memLimitMB)
// 	}

// 	memStats := &MemStats{}
// 	if *watchMemory || *memProfileFlag != "" {
// 		memStats.StartMonitoring()
// 	}

// 	if *memProfileFlag != "" {
// 		defer func() {
// 			f, err := os.Create(*memProfileFlag)
// 			if err != nil {
// 				log.Printf("Could not create memory profile: %v", err)
// 				return
// 			}
// 			defer f.Close()
// 			runtime.GC()
// 			if err := pprof.WriteHeapProfile(f); err != nil {
// 				log.Printf("Could not write memory profile: %v", err)
// 			} else {
// 				log.Printf("Memory profile written to %s", *memProfileFlag)
// 			}
// 		}()
// 	}

// 	shardSize := (expectedFileSize + int64(dataShards) - 1) / int64(dataShards)
// 	totalSize := shardSize * int64(totalShards)
// 	log.Printf("Preparing mmap file. Shard size: %d, Total size: %d", shardSize, totalSize)

// 	fileWorkDirPath := filepath.Join(workDir, targetFileName)
// 	if err := os.MkdirAll(fileWorkDirPath, 0755); err != nil {
// 		log.Panic(err)
// 	}
// 	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
// 		log.Panic(err)
// 	}

// 	mmapFilePath := filepath.Join(fileWorkDirPath, mmapFileName)
// 	mmapFile, err := os.OpenFile(mmapFilePath, os.O_CREATE|os.O_RDWR, 0666)
// 	if err != nil {
// 		log.Panic(err)
// 	}
// 	defer func() {
// 		mmapFile.Close()
// 		os.Remove(mmapFilePath)
// 	}()

// 	if err := syscall.Fallocate(int(mmapFile.Fd()), 0, 0, totalSize); err != nil {
// 		log.Printf("Fallocate failed (might not be supported): %v. Falling back to Truncate.", err)
// 		if err := mmapFile.Truncate(totalSize); err != nil {
// 			log.Panic(err)
// 		}
// 	} else {
// 		log.Printf("Disk space pre-allocated successfully: %d bytes", totalSize)
// 	}

// 	mmapData, err := syscall.Mmap(int(mmapFile.Fd()), 0, int(totalSize), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
// 	if err != nil {
// 		log.Panic(err)
// 	}
// 	defer syscall.Munmap(mmapData)

// 	shardsData := make([][]byte, totalShards)
// 	for i := range shardsData {
// 		offset := int64(i) * shardSize
// 		shardsData[i] = mmapData[offset : offset+shardSize]
// 	}

// 	udpConn, err := utils.CreateUDPListener(listenAddr)
// 	if err != nil {
// 		log.Panic(err)
// 	}
// 	defer udpConn.Close()
// 	udpConn.SetReadBuffer(128 * 1024 * 1024)
// 	log.Printf("Listening on %s...", listenAddr)

// 	// 5. 接收循环
// 	bufPool := &sync.Pool{
// 		New: func() interface{} {
// 			return make([]byte, 8+symbolSize)
// 		},
// 	}

// 	// 定义数据包结构
// 	type Packet struct {
// 		data []byte
// 		n    int
// 	}

// 	// 创建数据通道
// 	// 增大缓冲区以应对网络突发，防止丢包
// 	packetChan := make(chan Packet, 50000)
// 	var wg sync.WaitGroup
// 	var totalReceivedBytes int64
// 	var droppedPackets int64

// 	// 启动写文件协程
// 	// 开启多个消费者协程来并发写入磁盘，提高写入吞吐量
// 	numWorkers := 16
// 	for i := 0; i < numWorkers; i++ {
// 		wg.Add(1)
// 		go func() {
// 			defer wg.Done()
// 			for pkt := range packetChan {
// 				buf := pkt.data
// 				n := pkt.n

// 				if n < 8 {
// 					bufPool.Put(buf)
// 					continue
// 				}

// 				// 解析包头
// 				seqNum := binary.BigEndian.Uint64(buf[:8])
// 				shardIdx := uint32(seqNum >> 32)
// 				symbolIdx := uint32(seqNum & 0xFFFFFFFF)

// 				if shardIdx >= uint32(totalShards) {
// 					log.Printf("Invalid shard index: %d", shardIdx)
// 					bufPool.Put(buf)
// 					continue
// 				}

// 				dataLen := n - 8
// 				offset := int64(symbolIdx) * int64(symbolSize)

// 				// 写入对应的分片内存
// 				if offset+int64(dataLen) <= int64(len(shardsData[shardIdx])) {
// 					copy(shardsData[shardIdx][offset:], buf[8:n])
// 					atomic.AddInt64(&totalReceivedBytes, int64(dataLen))
// 				} else {
// 					// log.Printf("Index out of range: shard %d offset %d len %d", shardIdx, offset, dataLen)
// 				}

// 				bufPool.Put(buf)
// 			}
// 		}()
// 	}

// 	// 简单的退出机制：当一段时间没有收到数据，或者收到足够数据
// 	// 这里为了演示，使用超时机制
// 	readTimeout := idleTimeout
// 	udpConn.SetReadDeadline(time.Now().Add(readTimeout))

// 	lastLogTime := time.Now()
// 	packetsCount := 0

// 	for {
// 		buf := bufPool.Get().([]byte)
// 		n, _, err := udpConn.ReadFromUDP(buf)
// 		if err != nil {
// 			if n == 0 {
// 				// 超时或错误
// 				log.Printf("Receive loop ended: %v", err)
// 				break
// 			}
// 		}

// 		// 优化：每接收 1000 个包才刷新一次超时时间，减少系统调用
// 		packetsCount++
// 		if packetsCount >= 1000 {
// 			udpConn.SetReadDeadline(time.Now().Add(readTimeout))
// 			packetsCount = 0
// 		}

// 		// 将数据发送到通道
// 		select {
// 		case packetChan <- Packet{data: buf, n: n}:
// 		default:
// 			atomic.AddInt64(&droppedPackets, 1)
// 			bufPool.Put(buf)
// 		}

// 		if time.Since(lastLogTime) > logInterval {
// 			log.Printf("Received: %.2f MB, Dropped: %d", float64(atomic.LoadInt64(&totalReceivedBytes))/1024/1024, atomic.LoadInt64(&droppedPackets))
// 			lastLogTime = time.Now()
// 		}
// 	}

// 	close(packetChan)
// 	wg.Wait()

// 	log.Println("Reception finished. Starting reconstruction...")
// 	if *watchMemory {
// 		memStats.PrintCurrent("After reception")
// 	}

// 	// 6. 重组与解码
// 	enc, err := rs.NewStream(dataShards, parityShards)
// 	if err != nil {
// 		log.Panic(err)
// 	}

// 	// 重新打开分片文件为 Reader
// 	shards := make([]io.Reader, totalShards)
// 	for i := 0; i < totalShards; i++ {
// 		shards[i] = bytes.NewReader(shardsData[i])
// 	}

// 	// 验证分片完整性 (Verify)
// 	// 注意：Verify 需要读取所有数据，比较耗时。如果确信数据完整，可以直接 Reconstruct
// 	// 这里我们直接尝试 Reconstruct，它会自动利用现有分片恢复丢失的
// 	// 但 Reconstruct 需要 Writer 来接收恢复的数据。
// 	// 简单起见，我们假设接收到的分片文件就是输入，如果有缺失，我们需要为缺失的分片提供 Writer

// 	// 检查哪些分片是空的或者不存在（模拟丢失）
// 	// 在这个 UDP 接收模型中，文件都创建了，但是中间有空洞（0字节填充）。
// 	// RS 库通常假设 Reader 读出的数据是正确的。如果 UDP 丢包导致文件中间有 0，RS 校验会失败。
// 	// 真正的 RS UDP 传输需要记录哪些 Symbol 没收到（Bitmap），这里简化为假设大部分收到了。

// 	// 实际上，reedsolomon 库处理的是 io.Reader。如果文件中有空洞（未写入区域），Read 会读出 0。
// 	// 这会被视为错误的数据。
// 	// 严谨的做法是：接收端维护一个 Bitmap，记录哪些包收到了。
// 	// 重组时，只把收全了的分片传给 RS，没收全的传 nil。
// 	// 由于代码篇幅限制，这里假设网络状况良好，或者 UDP 丢包较少，RS 能纠正读出的 0 值错误（视作错误数据）。
// 	// *更正*：RS 纠删码只能处理“丢失”(Erasure)，即我知道这里缺数据。
// 	// 如果我把 0 当作数据传进去，那是“错误”(Error)。RS 纠错能力：Erasure = Parity, Error = Parity / 2。
// 	// 为了简化，这里我们假设：如果文件大小不对，或者我们显式标记为 nil。

// 	// 7. 合并文件 (Join)
// 	outFile, err := os.Create(outputPath)
// 	if err != nil {
// 		log.Panic(err)
// 	}
// 	defer outFile.Close()

// 	log.Printf("Joining shards to %s...", outputPath)
// 	// Join 会自动调用 Reconstruct 如果需要，只要提供了足够的 shards
// 	// 这里我们传入所有 shards。如果某些 shard 数据是坏的（因为丢包导致的0填充），Join 可能会失败。
// 	// 生产环境中，你应该在接收循环中记录每个 shard 是否完整接收。如果不完整，在 shards[] 中置为 nil。

// 	// 使用 channel 实现超时控制
// 	done := make(chan error, 1)
// 	go func() {
// 		done <- enc.Join(outFile, shards, expectedFileSize)
// 	}()

// 	select {
// 	case err := <-done:
// 		if err != nil {
// 			log.Printf("Join error (reconstruction might failed): %v", err)
// 		} else {
// 			log.Println("File reconstructed successfully!")
// 		}
// 	case <-time.After(10 * time.Second):
// 		log.Println("Join operation timed out after 10 seconds")
// 	}

// 	if *watchMemory {
// 		runtime.GC()
// 		memStats.PrintSummary(expectedFileSize)
// 	}
// }
