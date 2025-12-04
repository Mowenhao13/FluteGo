package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sync"
	"time"

	rs "github.com/klauspost/reedsolomon"
	"golang.org/x/time/rate"
)

var (
	dataShardsFlag   = flag.Int("data", 12, "Number of data shards to split into")
	parityShardsFlag = flag.Int("par", 4, "Number of parity shards")
	symbolSizeFlag   = flag.Int("symbol", 1400, "Symbol payload size in bytes")
	serverAddrFlag   = flag.String("addr", "192.168.1.102:3400", "UDP address of RS receiver")
	sendFileFlag     = flag.String("file", "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1024mb.bin", "File to encode and send")
	workDirFlag      = flag.String("workdir", "/home/Halllo/Projects/Flute_test_v2/cmd/work_files/", "Work directory to store temporary shards")
	bandwidthFlag    = flag.Int("bandwidth", 500, "Target bandwidth in Mbps for rate limiting")
	memProfileFlag   = flag.String("memprofile", "rs_sender_mem_profile.pprof", "Write memory profile to `file` (empty to disable)")
	watchMemory      = flag.Bool("watchmem", false, "Monitor memory usage during processing")
	memLimitMB       = flag.Int("memlimit", 0, "Set memory limit in MB (0 = no limit)")
)

type MemStats struct {
	StartAlloc uint64
	MaxAlloc   uint64
	StartTime  time.Time
}

func (m *MemStats) StartMonitoring() {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	m.StartAlloc = mem.Alloc
	m.MaxAlloc = mem.Alloc
	m.StartTime = time.Now()

	if *watchMemory {
		go m.monitorMemory()
	}
}

func (m *MemStats) monitorMemory() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastAlloc uint64

	for range ticker.C {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		if mem.Alloc > m.MaxAlloc {
			m.MaxAlloc = mem.Alloc
		}

		current := mem.Alloc - m.StartAlloc
		if !*watchMemory {
			continue
		}
		if current == lastAlloc {
			continue
		}
		lastAlloc = current
		fmt.Printf("\r[Memory] Current: %.2f MB, Peak: %.2f MB",
			float64(current)/1024/1024,
			float64(m.MaxAlloc-m.StartAlloc)/1024/1024)
	}
}

func (m *MemStats) PrintCurrent(phase string) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	current := mem.Alloc - m.StartAlloc
	fmt.Printf("[%s] Memory: Current=%.2f MB, Heap=%.2f MB, Sys=%.2f MB\n",
		phase,
		float64(current)/1024/1024,
		float64(mem.HeapAlloc)/1024/1024,
		float64(mem.Sys)/1024/1024)
}

func (m *MemStats) PrintSummary(fileSize int64) {
	elapsed := time.Since(m.StartTime)
	speed := float64(fileSize) / 1024 / 1024 / elapsed.Seconds()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	fmt.Printf("\n=== Encoding Memory Usage Summary ===\n")
	fmt.Printf("Processing time: %v\n", elapsed)
	fmt.Printf("Speed: %.2f MB/s\n", speed)
	fmt.Printf("Peak memory usage: %.2f MB\n", float64(m.MaxAlloc-m.StartAlloc)/1024/1024)
	fmt.Printf("Final memory usage: %.2f MB\n", float64(mem.Alloc-m.StartAlloc)/1024/1024)
	fmt.Printf("Total heap: %.2f MB\n", float64(mem.HeapAlloc)/1024/1024)
	fmt.Printf("System memory: %.2f MB\n", float64(mem.Sys)/1024/1024)
	fmt.Printf("Garbage collections: %d\n", mem.NumGC)
}

func main() {
	flag.Parse()

	dataShards := *dataShardsFlag
	parityShards := *parityShardsFlag
	if dataShards <= 0 || parityShards <= 0 {
		log.Fatalf("invalid shard parameters: data=%d parity=%d", dataShards, parityShards)
	}
	totalShards := dataShards + parityShards
	if totalShards > 256 {
		log.Fatalf("sum of shards cannot exceed 256 (got %d)", totalShards)
	}

	symbolSize := *symbolSizeFlag
	if symbolSize <= 0 {
		log.Fatalf("symbol size must be positive")
	}
	serverAddr := *serverAddrFlag
	sendFilePath := *sendFileFlag
	workDir := *workDirFlag
	bandwidthMbps := *bandwidthFlag
	if bandwidthMbps <= 0 {
		log.Fatalf("bandwidth must be positive Mbps (got %d)", bandwidthMbps)
	}

	if *memLimitMB > 0 {
		debug.SetMemoryLimit(int64(*memLimitMB) * 1024 * 1024)
		log.Printf("Memory limit set to %d MB", *memLimitMB)
	}

	memStats := &MemStats{}
	if *watchMemory || *memProfileFlag != "" {
		memStats.StartMonitoring()
	}

	if *memProfileFlag != "" {
		defer func() {
			f, err := os.Create(*memProfileFlag)
			if err != nil {
				log.Printf("Could not create memory profile: %v", err)
				return
			}
			defer f.Close()
			runtime.GC()
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Printf("Could not write memory profile: %v", err)
			} else {
				log.Printf("Memory profile written to %s", *memProfileFlag)
			}
		}()
	}

	file, err := os.Open(sendFilePath)
	if err != nil {
		log.Fatalf("打开文件失败: %v", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		log.Fatalf("获取文件信息失败: %v", err)
	}
	fileSize := stat.Size()
	log.Printf("File size: %d bytes", fileSize)
	if *watchMemory {
		memStats.PrintCurrent("After opening file")
	}

	remoteAddr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		log.Fatalf("解析目标地址失败: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, remoteAddr)
	if err != nil {
		log.Fatalf("建立UDP连接失败: %v", err)
	}
	defer conn.Close()
	conn.SetWriteBuffer(32 * 1024 * 1024)

	_, fn := filepath.Split(sendFilePath)
	fileWorkDirPath := filepath.Join(workDir, fn)
	if _, err := os.Stat(fileWorkDirPath); err == nil {
		os.RemoveAll(fileWorkDirPath)
	}
	if err := os.MkdirAll(fileWorkDirPath, 0755); err != nil {
		log.Panic(err)
	}

	enc, err := rs.NewStream(dataShards, parityShards)
	if err != nil {
		log.Panic(err)
	}

	out := make([]*os.File, totalShards)
	writers := make([]io.Writer, totalShards)
	for i := range out {
		outfn := fmt.Sprintf("%s.%d", fn, i)
		fullPath := filepath.Join(fileWorkDirPath, outfn)
		out[i], err = os.Create(fullPath)
		if err != nil {
			log.Panic(err)
		}
		writers[i] = out[i]
	}

	log.Println("Splitting file and encoding parity...")
	dataWriters := writers[:dataShards]
	if err := enc.Split(file, dataWriters, fileSize); err != nil {
		log.Panic(err)
	}
	if *watchMemory {
		memStats.PrintCurrent("After splitting")
	}

	inputReaders := make([]io.Reader, dataShards)
	for i := 0; i < dataShards; i++ {
		out[i].Close()
		f, err := os.Open(out[i].Name())
		if err != nil {
			log.Panic(err)
		}
		defer f.Close()
		inputReaders[i] = f
	}

	parityWriters := writers[dataShards:]
	if err := enc.Encode(inputReaders, parityWriters); err != nil {
		log.Panic(err)
	}
	if *watchMemory {
		runtime.GC()
		memStats.PrintCurrent("After encoding parity")
	}

	for i := dataShards; i < totalShards; i++ {
		out[i].Close()
	}

	log.Println("Start sending shards...")
	bytesPerSecond := (bandwidthMbps * 1000 * 1000) / 8
	limiter := rate.NewLimiter(rate.Limit(float64(bytesPerSecond)), 1024*1024)
	ctx := context.Background()

	bufPool := &sync.Pool{
		New: func() interface{} {
			return make([]byte, 8+symbolSize)
		},
	}

	shardFiles := make([]*os.File, totalShards)
	for i := 0; i < totalShards; i++ {
		path := filepath.Join(fileWorkDirPath, fmt.Sprintf("%s.%d", fn, i))
		f, err := os.Open(path)
		if err != nil {
			log.Panic(err)
		}
		defer f.Close()
		shardFiles[i] = f
	}

	shardInfo, err := shardFiles[0].Stat()
	if err != nil {
		log.Panic(err)
	}
	shardSize := shardInfo.Size()
	symbolSize64 := int64(symbolSize)
	totalSymbolsPerShard := (shardSize + symbolSize64 - 1) / symbolSize64

	totalSentBytes := int64(0)
	startTime := time.Now()

	for symIdx := int64(0); symIdx < totalSymbolsPerShard; symIdx++ {
		for shardIdx := 0; shardIdx < totalShards; shardIdx++ {
			f := shardFiles[shardIdx]

			offset := symIdx * symbolSize64
			if offset >= shardSize {
				continue
			}

			readSize := symbolSize
			if offset+int64(readSize) > shardSize {
				readSize = int(shardSize - offset)
			}

			buf := bufPool.Get().([]byte)
			seqNum := (uint64(shardIdx) << 32) | uint64(symIdx)
			binary.BigEndian.PutUint64(buf[:8], seqNum)

			n, err := f.ReadAt(buf[8:8+readSize], offset)
			if err != nil && err != io.EOF {
				log.Printf("Read error: %v", err)
				bufPool.Put(buf)
				continue
			}
			if n == 0 {
				bufPool.Put(buf)
				continue
			}

			packet := buf[:8+n]
			if err := limiter.WaitN(ctx, len(packet)); err != nil {
				log.Printf("Rate limiter error: %v", err)
			}

			if _, err := conn.Write(packet); err != nil {
				log.Printf("Send error: %v", err)
			} else {
				totalSentBytes += int64(n)
			}

			bufPool.Put(buf)
		}
	}

	duration := time.Since(startTime)
	mbps := float64(totalSentBytes) / 1024 / 1024 / duration.Seconds()
	log.Printf("Send complete. Total sent: %d bytes in %v (%.2f MB/s)", totalSentBytes, duration, mbps)

	if *watchMemory {
		runtime.GC()
		memStats.PrintSummary(fileSize)
	}
}
