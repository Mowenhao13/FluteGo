package test

import (
	pool "FluteGo/pkg/pool"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/reedsolomon"
	raptorq "github.com/xssnick/raptorq"
	"golang.org/x/sys/unix"
	"golang.org/x/time/rate"
)

const (
	workFilePath     = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1024mb.bin"
	redundancyRatio  = 1.01
	nogcTestFilePath = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_50mb.bin"
)

func TestEncInPlaceWithGC(t *testing.T) {
	var packetBufferPool = sync.Pool{
		New: func() interface{} {
			// 预分配略大于最大可能包（1408 + 8 = 1416）
			return make([]byte, 0, 1500)
		},
	}

	// 创建内存profile文件
	memProfile, err := os.Create("enc_inplace_mem.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
	runtime.GC() // 先进行垃圾回收
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write initial heap profile: %v", err)
	}

	globalPool := pool.GetGlobalPool()
	if globalPool == nil {
		t.Fatalf("Pool not initialized\n")
	}

	udpConn, err := globalPool.GetGlobalConnection(destIP, port)
	if err != nil {
		t.Fatalf("Failed to get the connection\n")
	}
	defer globalPool.ReturnConnection(udpConn)

	updatePeakMemory()
	file, err := os.OpenFile(workFilePath, os.O_RDWR, 0644)
	if err != nil {
		t.Errorf("Failed to open file: %v", err)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		t.Errorf("Failed to get file info: %v", err)
		return
	}
	fileSize := info.Size()

	// 记录发送过程中的内存分配
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	fd := int(file.Fd())
	chunkSz := 1024 * 1024
	sent := 0

	var pageSize = os.Getpagesize()

	updatePeakMemory()

	if chunkSz%pageSize != 0 {
		chunkSz = ((chunkSz + pageSize - 1) / pageSize) * pageSize
		t.Logf("Adjusted chunk size to %d (page size: %d)", chunkSz, pageSize)
	}

	for i := int64(0); i < fileSize; i += int64(chunkSz) {
		end := i + int64(chunkSz)
		remain := fileSize - i
		mapSize := chunkSz
		if end > fileSize {
			end = fileSize
			mapSize = int(remain)
		}

		chunkIdx := (i + int64(chunkSz) - 1) / int64(chunkSz)

		data, err := unix.Mmap(fd, i, mapSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			t.Fatalf("MMAP failed at offset %d: %v", i, err)
			continue
		}
		rq := raptorq.NewRaptorQ(1400)
		enc, err := rq.CreateEncoder(data)
		if err != nil {
			t.Fatalf("Failed to create raptorq encoder: %v", err)
			continue
		}

		baseSyms := enc.BaseSymbolsNum()
		totalSyms := uint32(float64(baseSyms) * redundancyRatio)
		fmt.Printf("BaseSym: %d, totalSym: %d\n", baseSyms, totalSyms)
		for j := uint32(0); j < totalSyms; j++ {
			symData := enc.GenSymbol(j)
			if symData == nil {
				fmt.Printf("Failed to generate symbol %d|%d\n", chunkIdx, j)
				continue
			}

			// --- 使用 sync.Pool ---
			buf := packetBufferPool.Get().([]byte)
			needed := 8 + len(symData)

			if cap(buf) < needed {
				buf = make([]byte, needed) // fallback
			} else {
				buf = buf[:needed] // ✅ 关键：设置正确长度
			}

			sequenceNumber := uint64(chunkIdx)*uint64(chunkSz) + uint64(j)
			binary.BigEndian.PutUint64(buf[:8], sequenceNumber)
			copy(buf[8:], symData)

			updatePeakMemory()

			// 发送（必须在 Put 之前！）
			actualWrite, err := udpConn.Conn.Write(buf)
			packetBufferPool.Put(buf[:0]) // ✅ 发完立刻归还（即使 err 也应 Put，但这里 panic）

			if err != nil {
				panic(err)
			}

			sent += actualWrite
			if sent%(500*1024) == 0 {
				var memStats runtime.MemStats
				runtime.ReadMemStats(&memStats)
				t.Logf("已发送: %d MB, 内存分配: %v MB",
					sent/(1024*1024),
					memStats.Alloc/(1024*1024))
				// time.Sleep(1 * time.Millisecond)
			}
			updatePeakMemory()
		}
		fmt.Printf("Generated chunk symbol %d\n", chunkIdx)

		if err := unix.Munmap(data); err != nil {
			t.Errorf("Munmap failed: %v", err)
			return
		}
	}

	runtime.ReadMemStats(&memStatsEnd)

	// 写入最终的内存profile
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 输出内存分析结果
	t.Logf("内存分析结果:")
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024*1024))
	t.Logf("峰值内存使用: %v MB", memStatsEnd.HeapAlloc/(1024*1024))
	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)

	finalPeak := atomic.LoadUint64(&globalPeakHeapAlloc)
	t.Logf("🔥 峰值内存使用: %v MB (%v bytes)", finalPeak/(1024*1024), finalPeak)
	t.Logf("📊 当前内存使用: %v MB", memStatsStart.HeapAlloc/(1024*1024))

	fmt.Printf("Generated all chunks\n")
}

func TestEncInPlaceWithGoroutines(t *testing.T) {
	const maxWorkers = 32 // 可根据 CPU 核心数调整

	var packetBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 0, 1500)
		},
	}

	memProfile, err := os.Create("enc_inplace_mem.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write initial heap profile: %v", err)
	}

	globalPool := pool.GetGlobalPool()
	if globalPool == nil {
		t.Fatalf("Pool not initialized\n")
	}

	udpConn, err := globalPool.GetGlobalConnection(destIP, port)
	if err != nil {
		t.Fatalf("Failed to get the connection\n")
	}
	defer globalPool.ReturnConnection(udpConn)

	updatePeakMemory()
	file, err := os.OpenFile(workFilePath, os.O_RDWR, 0644)
	if err != nil {
		t.Errorf("Failed to open file: %v", err)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		t.Errorf("Failed to get file info: %v", err)
		return
	}
	fileSize := info.Size()

	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	fd := int(file.Fd())
	chunkSz := 1024 * 1024
	var sent int64 // 改为 int64 并用原子操作

	pageSize := os.Getpagesize()
	if chunkSz%pageSize != 0 {
		chunkSz = ((chunkSz + pageSize - 1) / pageSize) * pageSize
		t.Logf("Adjusted chunk size to %d (page size: %d)", chunkSz, pageSize)
	}

	// 计算总 chunk 数
	numChunks := (fileSize + int64(chunkSz) - 1) / int64(chunkSz)
	t.Logf("Total chunks: %d", numChunks)

	// 控制并发数
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	var sendMu sync.Mutex // 仅用于打印日志时保护 sent（非必须）

	for i := int64(0); i < fileSize; i += int64(chunkSz) {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()

			// 限流
			sem <- struct{}{}
			defer func() { <-sem }()

			// 计算 chunkIdx 和 mapSize
			remain := fileSize - offset
			mapSize := chunkSz
			if remain < int64(chunkSz) {
				mapSize = int(remain)
			}
			chunkIdx := offset / int64(chunkSz)

			// Mmap
			data, err := unix.Mmap(fd, offset, mapSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
			if err != nil {
				t.Errorf("MMAP failed at offset %d: %v", offset, err)
				return
			}
			defer unix.Munmap(data)

			// 编码
			rq := raptorq.NewRaptorQ(1400)
			enc, err := rq.CreateEncoder(data)
			if err != nil {
				t.Errorf("Failed to create raptorq encoder for chunk %d: %v", chunkIdx, err)
				return
			}

			baseSyms := enc.BaseSymbolsNum()
			totalSyms := uint32(float64(baseSyms) * redundancyRatio)
			t.Logf("Chunk %d: BaseSym=%d, TotalSym=%d", chunkIdx, baseSyms, totalSyms)

			// 发送所有 symbols
			for j := uint32(0); j < totalSyms; j++ {
				symData := enc.GenSymbol(j)
				if symData == nil {
					t.Logf("Failed to generate symbol %d|%d", chunkIdx, j)
					continue
				}

				// 构造包
				buf := packetBufferPool.Get().([]byte)
				needed := 8 + len(symData)
				if cap(buf) < needed {
					buf = make([]byte, needed)
				} else {
					buf = buf[:needed]
				}

				seqNum := uint64(chunkIdx)*uint64(chunkSz) + uint64(j)
				binary.BigEndian.PutUint64(buf[:8], seqNum)
				copy(buf[8:], symData)

				// 发送
				actualWrite, err := udpConn.Conn.Write(buf)
				packetBufferPool.Put(buf[:0])

				if err != nil {
					t.Errorf("Write failed for chunk %d symbol %d: %v", chunkIdx, j, err)
					return
				}

				// 原子增加 sent
				atomic.AddInt64(&sent, int64(actualWrite))

				// 打印进度（加锁避免日志混乱）
				if atomic.LoadInt64(&sent)%(500*1024) == 0 {
					sendMu.Lock()
					memStats := runtime.MemStats{}
					runtime.ReadMemStats(&memStats)
					t.Logf("已发送: %d MB, 内存分配: %v MB",
						atomic.LoadInt64(&sent)/(1024*1024),
						memStats.Alloc/(1024*1024))
					sendMu.Unlock()
				}
			}

			t.Logf("✅ Finished chunk %d", chunkIdx)
		}(i)
	}

	// 等待所有完成
	wg.Wait()

	runtime.ReadMemStats(&memStatsEnd)
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	t.Logf("内存分析结果:")
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024*1024))
	t.Logf("峰值内存使用: %v MB", memStatsEnd.HeapAlloc/(1024*1024))
	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)

	finalPeak := atomic.LoadUint64(&globalPeakHeapAlloc)
	t.Logf("🔥 峰值内存使用: %v MB (%v bytes)", finalPeak/(1024*1024), finalPeak)
	t.Logf("📊 总发送: %d MB", atomic.LoadInt64(&sent)/(1024*1024))
}

// Stream read
//     encode_test.go:606: 编码完成: 1098 MB, 包数: 843776, 耗时: 7.089001059s
//     encode_test.go:618: 内存分析结果:
//     encode_test.go:619: 总分配内存: 88 MB
//     encode_test.go:620: 峰值内存使用: 21 MB
//     encode_test.go:621: 垃圾回收次数: 7
// --- PASS: TestRsEncInPlaceWithGC_Old (7.17s)
// PASS
// ok      FluteGo/test    7.271s

// Mmap read
// 	   encode_test.go:656: 编码完成: 1331 MB, 包数: 997074, 耗时: 6.50546925s
//     encode_test.go:668: 内存分析结果:
//     encode_test.go:669: 总分配内存: 5 MB
//     encode_test.go:670: 峰值内存使用: 5 MB
//     encode_test.go:671: 垃圾回收次数: 1
// --- PASS: TestRsEncInPlaceWithGC_Old (6.54s)

func TestRsEncInPlaceWithGC(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("rs_enc_mem_profile.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write initial heap profile: %v", err)
	}

	// 配置参数
	const (
		dataShards           = 12
		parityShards         = 4
		totalShards          = dataShards + parityShards
		symbolSize           = 1400
		indexSize            = 8
		chunkSize            = 1 * 1024 * 1024 // 10MB per chunk
		networkBandwidthMbps = 1000             // 网线带宽上限
	)

	bytesPerSec := networkBandwidthMbps * 1_000_000 / 8 // Mbps -> bytes per second
	burst := bytesPerSec / 10
	packetBytes := indexSize + symbolSize
	if burst < packetBytes {
		burst = packetBytes
	}
	rateLimiter := rate.NewLimiter(rate.Limit(float64(bytesPerSec)), burst)
	t.Logf("发送速率限制: %d Mbps (~%d bytes/sec), burst=%d bytes", networkBandwidthMbps, bytesPerSec, burst)

	// 获取UDP连接
	globalPool := pool.GetGlobalPool()
	if globalPool == nil {
		t.Fatalf("Pool not initialized\n")
	}
	conn, err := globalPool.GetGlobalConnection(destIP, port)
	if err != nil {
		t.Fatalf("Failed to get the connection\n")
	}
	defer globalPool.ReturnConnection(conn)

	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	// 创建RS编码器 (Block模式)
	enc, err := reedsolomon.New(dataShards, parityShards)
	if err != nil {
		t.Fatalf("创建RS编码器失败: %v", err)
	}

	// 打开工作文件
	testFile, err := os.OpenFile(workFilePath, os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("打开工作文件失败: %v", err)
	}
	defer testFile.Close()

	info, err := testFile.Stat()
	if err != nil {
		t.Fatalf("获取文件信息失败: %v", err)
	}
	fileSize := info.Size()
	t.Logf("使用文件: %s, 大小: %d MB", workFilePath, fileSize/(1024*1024))

	// mmap 映射文件
	fd := int(testFile.Fd())
	// 使用 MAP_PRIVATE | PROT_WRITE 以避免 "unexpected fault address" 错误
	// 某些 optimized assembly 可能需要写权限，或者 alignment fixup
	mmapData, err := unix.Mmap(fd, 0, int(fileSize), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE)
	if err != nil {
		t.Fatalf("mmap失败: %v", err)
	}
	defer unix.Munmap(mmapData)

	t.Log("开始分块编码并发送...")
	startTime := time.Now()

	var totalSent int64
	var totalPackets int64

	// 缓冲区
	packet := make([]byte, indexSize+symbolSize)
	var paddingBuf []byte

	// 计算总chunk数
	totalChunks := (fileSize + int64(chunkSize) - 1) / int64(chunkSize)

	// 循环处理每个Chunk
	for chunkIdx := int64(0); chunkIdx < totalChunks; chunkIdx++ {
		offset := chunkIdx * int64(chunkSize)
		end := offset + int64(chunkSize)
		if end > fileSize {
			end = fileSize
		}

		// 获取当前chunk的数据切片
		chunkData := mmapData[offset:end]

		// 检查是否需要填充 (最后一个chunk可能不对齐)
		// 为了保证接收端能够正确解码，必须填充到完整的 chunkSize
		if len(chunkData) < chunkSize {
			// 需要填充，必须拷贝到缓冲区
			targetSize := chunkSize

			if cap(paddingBuf) < targetSize {
				paddingBuf = make([]byte, targetSize)
			}
			paddingBuf = paddingBuf[:targetSize]

			copy(paddingBuf, chunkData)
			// 填充0
			for i := len(chunkData); i < targetSize; i++ {
				paddingBuf[i] = 0
			}
			chunkData = paddingBuf
		}

		// 分割数据 (Split会自动填充0)
		shards, err := enc.Split(chunkData)
		if err != nil {
			t.Fatalf("Split失败: %v", err)
		}

		// 编码生成parity
		err = enc.Encode(shards)
		if err != nil {
			t.Fatalf("Encode失败: %v", err)
		}

		// 发送该Chunk的所有shards
		// 计算每个shard的大小 (Split后所有shard大小一致)
		shardSize := len(shards[0])
		totalSymbolsPerShard := (shardSize + symbolSize - 1) / symbolSize

		for symbolIdx := 0; symbolIdx < totalSymbolsPerShard; symbolIdx++ {
			for shardIdx, shard := range shards {
				start := symbolIdx * symbolSize
				end := start + symbolSize
				if end > len(shard) {
					end = len(shard)
				}

				// 构造包头: ChunkIdx (16) | ShardIdx (16) | SymbolIdx (32)
				seq := (uint64(chunkIdx) << 48) | (uint64(shardIdx) << 32) | uint64(symbolIdx)
				binary.BigEndian.PutUint64(packet[0:8], seq)

				// 复制数据
				copy(packet[8:], shard[start:end])

				payloadLen := end - start
				rateLimiter.WaitN(context.Background(), 8+payloadLen)
				_, err := conn.Conn.Write(packet[:8+payloadLen])
				if err != nil {
					t.Fatalf("发送失败: %v", err)
				}

				totalSent += int64(payloadLen)
				totalPackets++
			}
		}

		if chunkIdx%10 == 0 {
			t.Logf("Chunk %d 编码完成", chunkIdx)
		}
	}

	t.Logf("编码完成: %d MB, 包数: %d, 耗时: %v",
		totalSent/(1024*1024), totalPackets, time.Since(startTime))

	runtime.ReadMemStats(&memStatsEnd)

	// 写入最终的内存profile
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 输出内存分析结果
	t.Logf("=== 内存性能分析结果 ===")
	t.Logf("总分配内存: %v bytes", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
	t.Logf("峰值堆内存: %v bytes, %v MB", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
	t.Logf("系统申请内存 (Sys): %d MB", memStatsEnd.Sys/(1024*1024))
	t.Logf("堆空闲内存 (HeapIdle): %d MB", memStatsEnd.HeapIdle/(1024*1024))
	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
	t.Logf("内存分配次数: %v", memStatsEnd.Mallocs-memStatsStart.Mallocs)
	t.Logf("堆对象数量: %v", memStatsEnd.HeapObjects)
}

// NoGC test result (50mb file)
// Generated chunk symbol 4266
//     encode_test.go:486: Chunk 4266 完成: 累计分配 1317 MB, 当前堆内存 1317 MB
//     encode_test.go:500: 内存分析结果:
//     encode_test.go:501: 总分配内存: 1315 MB
//     encode_test.go:502: 峰值内存使用: 1317 MB
//     encode_test.go:503: 符号数量: 38400
//     encode_test.go:504: 总数据量: 51 MB
//     encode_test.go:507: 触发GC前堆内存: 1317 MB
//     encode_test.go:510: 触发GC后堆内存: 53 MB
// Generated all chunks
// --- PASS: TestEncInPlaceNoGC (1.37s)

func TestEncInPlaceNoGC(t *testing.T) {
	// 完全禁用GC
	debug.SetGCPercent(-1)
	defer debug.SetGCPercent(100) // 测试结束后恢复

	// 创建内存profile文件
	memProfile, err := os.Create("enc_inplace_mem.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write initial heap profile: %v", err)
	}

	globalPool := pool.GetGlobalPool()
	if globalPool == nil {
		t.Fatalf("Pool not initialized\n")
	}

	file, err := os.OpenFile(nogcTestFilePath, os.O_RDWR, 0644)
	if err != nil {
		t.Errorf("Failed to open file: %v", err)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		t.Errorf("Failed to get file info: %v", err)
		return
	}
	fileSize := info.Size()

	// 记录发送过程中的内存分配
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	fd := int(file.Fd())
	chunkSz := 10 * 1024
	sent := 0

	var pageSize = os.Getpagesize()

	if chunkSz%pageSize != 0 {
		chunkSz = ((chunkSz + pageSize - 1) / pageSize) * pageSize
		t.Logf("Adjusted chunk size to %d (page size: %d)", chunkSz, pageSize)
	}

	// 用于保存所有生成的符号，防止被回收
	var allSymbols [][]byte
	var maxHeapAlloc uint64

	for i := int64(0); i < fileSize; i += int64(chunkSz) {
		end := i + int64(chunkSz)
		remain := fileSize - i
		mapSize := chunkSz
		if end > fileSize {
			end = fileSize
			mapSize = int(remain)
		}

		chunkIdx := (i + int64(chunkSz) - 1) / int64(chunkSz)

		data, err := unix.Mmap(fd, i, mapSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			t.Fatalf("MMAP failed at offset %d: %v", i, err)
			continue
		}
		rq := raptorq.NewRaptorQ(1400)
		enc, err := rq.CreateEncoder(data)
		if err != nil {
			t.Fatalf("Failed to create raptorq encoder: %v", err)
			continue
		}

		baseSyms := enc.BaseSymbolsNum()
		totalSyms := uint32(float64(baseSyms) * redundancyRatio)
		fmt.Printf("BaseSym: %d, totalSym: %d\n", baseSyms, totalSyms)

		for j := uint32(0); j < totalSyms; j++ {
			symData := enc.GenSymbol(j)
			if symData == nil {
				fmt.Printf("Failed to generate symbol %d|%d: %v\n", chunkIdx, j, err)
				continue
			}

			// 保存符号数据，防止GC回收
			symbolCopy := make([]byte, len(symData))
			copy(symbolCopy, symData)
			allSymbols = append(allSymbols, symbolCopy)

			sent += len(symData)

			// 监控内存使用
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)
			if memStats.HeapAlloc > maxHeapAlloc {
				maxHeapAlloc = memStats.HeapAlloc
			}

			if sent%(500*1024) == 0 {
				t.Logf("已发送: %d MB, 当前内存: %v MB, 峰值内存: %v MB",
					sent/(1024*1024),
					memStats.HeapAlloc/(1024*1024),
					maxHeapAlloc/(1024*1024))
			}
		}
		fmt.Printf("Generated chunk symbol %d\n", chunkIdx)

		if err := unix.Munmap(data); err != nil {
			t.Errorf("Munmap failed: %v", err)
			return
		}

		// 每处理完一个chunk报告内存情况
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		t.Logf("Chunk %d 完成: 累计分配 %v MB, 当前堆内存 %v MB",
			chunkIdx,
			memStats.TotalAlloc/(1024*1024),
			memStats.HeapAlloc/(1024*1024))
	}

	runtime.ReadMemStats(&memStatsEnd)

	// 写入最终的内存profile
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 输出内存分析结果
	t.Logf("内存分析结果:")
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024*1024))
	t.Logf("峰值内存使用: %v MB", maxHeapAlloc/(1024*1024))
	t.Logf("符号数量: %d", len(allSymbols))
	t.Logf("总数据量: %v MB", sent/(1024*1024))

	// 手动触发一次GC看看能回收多少
	t.Logf("触发GC前堆内存: %v MB", memStatsEnd.HeapAlloc/(1024*1024))
	runtime.GC()
	runtime.ReadMemStats(&memStatsEnd)
	t.Logf("触发GC后堆内存: %v MB", memStatsEnd.HeapAlloc/(1024*1024))

	fmt.Printf("Generated all chunks\n")

	// 保持allSymbols不被回收直到测试结束
	runtime.KeepAlive(allSymbols)
}
