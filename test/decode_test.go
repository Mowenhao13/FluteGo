package test

import (
	// "bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/reedsolomon"
	raptorq "github.com/xssnick/raptorq"
)

type offsetWriter struct {
	f   *os.File
	off int64
}

func (w *offsetWriter) Write(p []byte) (int, error) {
	n, err := w.f.WriteAt(p, w.off)
	if n > 0 {
		w.off += int64(n)
	}
	return n, err
}

// 全局状态 - 使用类似顺序写入的结构
type sharedState struct {
	decoderStates    sync.Map   // chunkIdx -> *chunkDecoderState
	fileMutex        sync.Mutex // 文件写入锁
	cleanupMutex     sync.Mutex // 清理锁
	totalReceived    int64      // 总接收字节数（已写入文件）
	totalPackets     int64      // 总包数
	expectedSequence uint64     // 期望的序列号（用于顺序控制）
	sequenceMutex    sync.Mutex // 保护序列号操作
	startTime        time.Time  // 开始时间
	lastDataTime     int64      // 最后数据时间
	outputFile       *os.File   // 输出文件
	decoderCount     int32      // decoder计数器
}

// 在 sharedState 结构体中添加方法
func (s *sharedState) writeChunkData(chunkIdx uint64, dataShards [][]byte, chunkSize int, dataShardsNum int, fileSize int64) error {
	s.fileMutex.Lock()
	defer s.fileMutex.Unlock()

	// 计算文件偏移
	fileOffset := int64(chunkIdx) * int64(chunkSize)

	// 计算实际要写入的数据大小（处理文件末尾）
	remainingFileSize := fileSize - fileOffset
	actualChunkSize := chunkSize
	if remainingFileSize < int64(chunkSize) {
		actualChunkSize = int(remainingFileSize)
	}
	if actualChunkSize <= 0 {
		return fmt.Errorf("chunk %d 写入大小无效: %d", chunkIdx, actualChunkSize)
	}

	// 计算每个shard的实际大小
	shardSize := actualChunkSize / dataShardsNum
	if actualChunkSize%dataShardsNum != 0 {
		shardSize++
	}

	// 获取shard大小 (假设所有shard大小相同，且已经对齐)
	if len(dataShards) > 0 {
		shardSize = len(dataShards[0])
	}

	// 合并数据shards
	totalWritten := 0
	for i := 0; i < dataShardsNum && totalWritten < actualChunkSize; i++ {
		// 计算当前shard要写入的大小
		remaining := actualChunkSize - totalWritten
		currentShardSize := shardSize
		if currentShardSize > remaining {
			currentShardSize = remaining
		}

		if currentShardSize <= 0 {
			break
		}

		shardData := dataShards[i]
		if len(shardData) > currentShardSize {
			shardData = shardData[:currentShardSize]
		}

		_, err := s.outputFile.WriteAt(shardData, fileOffset+int64(totalWritten))
		if err != nil {
			return fmt.Errorf("写入shard %d 失败: %v", i, err)
		}

		totalWritten += currentShardSize
	}

	atomic.AddInt64(&s.totalReceived, int64(totalWritten))

	// 及时释放内存
	for i := range dataShards {
		dataShards[i] = nil
	}

	fmt.Printf("chunk %d 数据写入完成: %d bytes", chunkIdx, totalWritten)
	return nil
}

func TestRqDecInPlace(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("req_dec_mem_profile.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write initial heap profile: %v", err)
	}

	// 创建UDP监听
	conn, err := net.ListenPacket("udp", ":"+strconv.Itoa(port))
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	udpConn := conn.(*net.UDPConn)
	t.Logf("开始监听UDP端口: %d", port)
	udpConn.SetReadBuffer(128 * 1024 * 1024)

	// 创建输出文件
	outputFile, err := os.Create("received_file.bin")
	if err != nil {
		panic(err)
	}
	defer outputFile.Close()

	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	// 配置参数（需要与编码端完全一致）
	const (
		chunkSz       = 1024 * 1024        // chunk大小
		symbolSize    = 1400               // 符号大小
		indexSize     = 8                  // 序列号大小
		maxPacketSize = 1400 + 8           // 最大包大小
		fileSize      = 1024 * 1024 * 1024 // 1GB文件大小
	)

	// 解码器状态管理
	type chunkDecoderState struct {
		decoder   *raptorq.Decoder
		received  int        // 已接收符号数
		expected  int        // 期望接收符号数
		chunkSize int        // chunk大小
		startSeq  uint64     // 起始序列号
		decoded   bool       // 是否已解码成功
		lastUsed  time.Time  // 最后使用时间
		mutex     sync.Mutex // 保护decoder状态
	}

	state := &sharedState{
		decoderStates:    sync.Map{},
		outputFile:       outputFile,
		startTime:        time.Now(),
		lastDataTime:     time.Now().UnixNano(),
		expectedSequence: 0,
	}

	// 计算chunk索引和符号索引
	getChunkAndSymbolIndex := func(sequenceNumber uint64) (chunkIdx uint64, symbolIdx uint32) {
		chunkIdx = sequenceNumber / uint64(chunkSz)
		symbolIdx = uint32(sequenceNumber % uint64(chunkSz))
		return chunkIdx, symbolIdx
	}

	// 估算需要的符号数量
	estimateRequiredSymbols := func(dataSize int) int {
		baseSymbols := (dataSize + symbolSize - 1) / symbolSize
		return int(float64(baseSymbols) * 1.01) // 1%冗余
	}

	// 计算chunk的实际大小
	calculateChunkSize := func(chunkIdx uint64) int {
		startOffset := int64(chunkIdx) * int64(chunkSz)
		if startOffset >= fileSize {
			return 0
		}

		endOffset := startOffset + int64(chunkSz)
		if endOffset > fileSize {
			return int(fileSize - startOffset)
		}
		return chunkSz
	}

	// 获取或创建chunk decoder
	getOrCreateDecoder := func(chunkIdx uint64) (*chunkDecoderState, error) {
		// 检查chunk是否有效
		actualChunkSize := calculateChunkSize(chunkIdx)
		if actualChunkSize <= 0 {
			return nil, fmt.Errorf("无效的chunk索引: %d", chunkIdx)
		}

		// 尝试获取现有的decoder
		if existing, exists := state.decoderStates.Load(chunkIdx); exists {
			dec := existing.(*chunkDecoderState)
			dec.lastUsed = time.Now()
			if dec.decoded {
				return nil, fmt.Errorf("chunk %d already decoded", chunkIdx)
			}
			return dec, nil
		}

		// 检查decoder数量限制
		currentCount := atomic.LoadInt32(&state.decoderCount)
		if currentCount > 1000 {
			t.Logf("警告: decoder数量接近限制 (%d)", currentCount)
		}

		// 创建新的decoder
		rq := raptorq.NewRaptorQ(uint32(symbolSize))
		dec, err := rq.CreateDecoder(uint32(actualChunkSize))
		if err != nil {
			return nil, fmt.Errorf("创建decoder失败: %v", err)
		}

		requiredSymbols := estimateRequiredSymbols(actualChunkSize)
		startSeq := chunkIdx * uint64(chunkSz)

		newDecoder := &chunkDecoderState{
			decoder:   dec,
			received:  0,
			expected:  requiredSymbols,
			chunkSize: actualChunkSize,
			startSeq:  startSeq,
			lastUsed:  time.Now(),
		}

		// 使用LoadOrStore确保只有一个decoder被创建
		existing, loaded := state.decoderStates.LoadOrStore(chunkIdx, newDecoder)
		if loaded {
			// 其他goroutine已经创建了decoder
			dec := existing.(*chunkDecoderState)
			dec.lastUsed = time.Now()
			if dec.decoded {
				return nil, fmt.Errorf("chunk %d already decoded", chunkIdx)
			}
			return dec, nil
		}

		// 增加decoder计数
		atomic.AddInt32(&state.decoderCount, 1)

		t.Logf("创建chunk %d解码器: 数据大小=%d, 需要符号数=%d",
			chunkIdx, actualChunkSize, requiredSymbols)

		return newDecoder, nil
	}

	// 实时写入解码数据到文件
	writeDecodedData := func(chunkIdx uint64, data []byte) error {
		offset := int64(chunkIdx) * int64(chunkSz)

		state.fileMutex.Lock()
		defer state.fileMutex.Unlock()

		updatePeakMemory2()

		n, err := state.outputFile.WriteAt(data, offset)
		if err != nil {
			return fmt.Errorf("写入文件失败: %v", err)
		}

		if n != len(data) {
			return fmt.Errorf("写入大小不匹配: 期望=%d, 实际=%d", len(data), n)
		}

		updatePeakMemory2()
		data = nil
		// 更新总接收字节数
		atomic.AddInt64(&state.totalReceived, int64(n))
		return nil
	}

	// 处理接收到的符号 - 实时写入版本
	processSymbol := func(sequenceNumber uint64, symbolData []byte) error {
		chunkIdx, symbolIdx := getChunkAndSymbolIndex(sequenceNumber)

		// 获取或创建decoder
		dec, err := getOrCreateDecoder(chunkIdx)
		if err != nil {
			if err.Error() == "chunk "+strconv.FormatUint(chunkIdx, 10)+" already decoded" {
				// 已经解码成功，正常忽略
				return nil
			}
			return err
		}

		dec.mutex.Lock()
		defer dec.mutex.Unlock()
		dec.lastUsed = time.Now()

		// 如果已经解码成功，忽略后续符号
		if dec.decoded {
			return nil
		}

		// 添加符号到decoder
		canTryDecode, err := dec.decoder.AddSymbol(symbolIdx, symbolData)
		if err != nil {
			return fmt.Errorf("添加符号失败: %v", err)
		}

		dec.received++

		// 定期报告chunk进度
		if dec.received%20 == 0 && dec.received > 0 {
			t.Logf("chunk %d: 已接收 %d/%d 个符号",
				chunkIdx, dec.received, dec.expected)
		}

		// 尝试解码
		if canTryDecode {
			success, result, err := dec.decoder.Decode()
			if err != nil {
				return fmt.Errorf("解码失败: %v", err)
			}

			if success {
				t.Logf("chunk %d 解码成功! 接收符号数: %d/%d",
					chunkIdx, dec.received, dec.expected)

				// 实时写入文件，不保存到内存
				if err := writeDecodedData(chunkIdx, result); err != nil {
					return err
				}

				dec.decoded = true
				dec.decoder = nil
				t.Logf("chunk %d 数据已实时写入文件, 偏移量: %d, 大小: %d",
					chunkIdx, int64(chunkIdx)*int64(chunkSz), len(result))
			} else {
				t.Logf("chunk %d 解码尝试失败，继续接收符号...", chunkIdx)
			}
		}

		return nil
	}

	// 统计已解码的chunk数量
	countDecodedChunks := func() (decoded, total int) {
		state.decoderStates.Range(func(key, value interface{}) bool {
			total++
			dec := value.(*chunkDecoderState)
			if dec.decoded {
				decoded++
			}
			return true
		})
		return decoded, total
	}

	updatePeakMemory2()
	// 清理已解码的decoder
	cleanupDecodedDecoders := func() int {
		state.cleanupMutex.Lock()
		defer state.cleanupMutex.Unlock()

		cleaned := 0
		state.decoderStates.Range(func(key, value interface{}) bool {
			dec := value.(*chunkDecoderState)
			if dec.decoded {
				// 解码成功后立即清理，不等待5秒
				state.decoderStates.Delete(key)
				atomic.AddInt32(&state.decoderCount, -1)
				cleaned++

				// 强制释放decoder资源
				if dec.decoder != nil {
					// 如果有释放资源的方法
					// dec.decoder.Close() 或 dec.decoder.Free()
					dec.decoder = nil
				}
			} else if time.Since(dec.lastUsed) > 30*time.Second {
				// 长时间未使用的decoder也清理
				state.decoderStates.Delete(key)
				atomic.AddInt32(&state.decoderCount, -1)
				cleaned++
				t.Logf("清理超时未使用的chunk decoder: %d", key.(uint64))
			}
			return true
		})
		return cleaned
	}

	// 使用sync.WaitGroup等待所有goroutine完成
	var wg sync.WaitGroup
	workerCount := 32 // 减少worker数量

	t.Logf("启动 %d 个goroutine进行并发接收和解码", workerCount)

	// 创建context用于协调goroutine退出
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动清理协程
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		updatePeakMemory2()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleaned := cleanupDecodedDecoders()
				if cleaned > 0 {
					t.Logf("清理了 %d 个已解码的decoder", cleaned)
				}
			}
		}
	}()

	// 使用sync.Pool复用缓冲区
	var bufPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, maxPacketSize)
		},
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)

		go func(workerID int) {
			defer wg.Done()

			// 复用缓冲区
			buf := bufPool.Get().([]byte)
			var symbolData []byte

			for {
				select {
				case <-ctx.Done():
					t.Logf("Worker %d 被取消", workerID)
					return
				default:
				}

				// 检查是否已完成
				totalReceived := atomic.LoadInt64(&state.totalReceived)
				if totalReceived >= fileSize {
					t.Logf("Worker %d: 文件接收完成", workerID)
					return
				}

				// 设置读取超时
				conn.SetReadDeadline(time.Now().Add(2 * time.Second))

				n, _, err := udpConn.ReadFromUDP(buf)
				if err != nil {
					if os.IsTimeout(err) {
						lastTime := time.Unix(0, atomic.LoadInt64(&state.lastDataTime))
						idleTime := time.Since(lastTime)

						if idleTime > 60*time.Second {
							t.Logf("Worker %d 超时退出，空闲: %v", workerID, idleTime)
							return
						}
						continue
					}
					t.Logf("Worker %d 读取错误: %v", workerID, err)
					continue
				}

				if n < indexSize {
					t.Logf("Worker %d: 包大小过小: %d bytes", workerID, n)
					continue
				}

				// 解析序列号和数据
				sequenceNumber := binary.BigEndian.Uint64(buf[0:indexSize])

				// 复用symbolData切片
				if cap(symbolData) < n-indexSize {
					symbolData = make([]byte, n-indexSize)
				} else {
					symbolData = symbolData[:n-indexSize]
				}
				copy(symbolData, buf[indexSize:n])

				// 更新统计信息
				atomic.AddInt64(&state.totalPackets, 1)
				atomic.StoreInt64(&state.lastDataTime, time.Now().UnixNano())

				// 处理符号
				if err := processSymbol(sequenceNumber, symbolData); err != nil {
					if err.Error() != "chunk already decoded" {
						t.Logf("Worker %d 处理符号失败: %v", workerID, err)
					}
				}

				// // 及时释放引用
				// symbolData = nil

				// 定期报告进度
				totalPackets := atomic.LoadInt64(&state.totalPackets)
				totalReceived = atomic.LoadInt64(&state.totalReceived)

				if totalPackets%50000 == 0 {
					var memStats runtime.MemStats
					runtime.ReadMemStats(&memStats)
					elapsed := time.Since(state.startTime).Seconds()
					rate := float64(totalReceived) / elapsed / (1024 * 1024)

					decodedChunks, totalChunks := countDecodedChunks()
					currentDecoders := atomic.LoadInt32(&state.decoderCount)

					t.Logf("进度: 接收=%d MB, 速率=%.2f MB/s, 解码chunks=%d/%d, decoder数=%d, 内存=%v MB",
						totalReceived/(1024*1024), rate, decodedChunks, totalChunks, currentDecoders,
						memStats.Alloc/(1024*1024))
				}
				// 及时清空引用
				symbolData = symbolData[:0]
			}
		}(i)
	}

	// 等待所有goroutine完成或超时
	go func() {
		wg.Wait()
		cancel()
	}()

	select {
	case <-time.After(300 * time.Second):
		t.Logf("测试超时")
		cancel()
	case <-ctx.Done():
		t.Logf("所有goroutine完成")
	}

	// 最终统计
	decodedChunks, totalChunks := countDecodedChunks()
	totalReceived := atomic.LoadInt64(&state.totalReceived)
	totalPackets := atomic.LoadInt64(&state.totalPackets)
	finalDecoderCount := atomic.LoadInt32(&state.decoderCount)

	t.Logf("=== 解码完成 ===")
	t.Logf("总接收数据: %d bytes (%d MB)", totalReceived, totalReceived/(1024*1024))
	t.Logf("总数据包数: %d", totalPackets)
	t.Logf("总chunks数: %d", totalChunks)
	t.Logf("成功解码chunks: %d", decodedChunks)
	t.Logf("剩余decoder数: %d", finalDecoderCount)
	if totalChunks > 0 {
		t.Logf("解码成功率: %.2f%%", float64(decodedChunks)/float64(totalChunks)*100)
	}
	t.Logf("总耗时: %v", time.Since(state.startTime))

	// 记录结束时的内存状态
	runtime.ReadMemStats(&memStatsEnd)

	// 写入最终的内存profile
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 输出内存分析结果
	t.Logf("内存分析结果:")
	t.Logf("总接收数据: %d MB", totalReceived/(1024*1024))
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024*1024))
	t.Logf("峰值内存使用: %v MB", memStatsEnd.HeapAlloc/(1024*1024))
	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)

	finalPeak := atomic.LoadUint64(&globalPeakHeapAlloc2)
	t.Logf("🔥 峰值内存使用: %v MB (%v bytes)", finalPeak/(1024*1024), finalPeak)
	t.Logf("📊 当前内存使用: %v MB", memStatsStart.HeapAlloc/(1024*1024))

	// 验证文件大小
	fileInfo, err := outputFile.Stat()
	if err != nil {
		t.Errorf("无法获取文件信息: %v", err)
	} else {
		actualSize := fileInfo.Size()
		t.Logf("实际文件大小: %d bytes (%d MB)", actualSize, actualSize/(1024*1024))

		if actualSize != fileSize {
			t.Errorf("文件大小不匹配: 期望=%d, 实际=%d", fileSize, actualSize)
		} else {
			t.Logf("文件大小验证成功!")
		}
	}

	t.Logf("请使用以下命令验证文件完整性:")
	t.Logf("md5sum received_file.bin")
	t.Logf("md5sum /path/to/original/file.bin")
}

func checkErr(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s", err.Error())
		os.Exit(2)
	}
}

func TestRsDecInPlace(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("rs_dec_mem_profile.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write initial heap profile: %v", err)
	}

	conn, err := net.ListenPacket("udp", ":"+strconv.Itoa(port))
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	udpConn := conn.(*net.UDPConn)
	udpConn.SetReadBuffer(128 * 1024 * 1024)
	t.Logf("开始监听UDP端口: %d", port)

	outputFile, err := os.Create("pi_v2_received_file.bin")
	if err != nil {
		panic(err)
	}
	defer outputFile.Close()

	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	const (
		dataShards       = 12
		parityShards     = 4
		totalShards      = dataShards + parityShards
		symbolSize       = 1400
		indexSize        = 8
		chunkSize        = 1 * 1024 * 1024    // 10MB
		fileSize         = 1024 * 1024 * 1024 // 1024MB
		packetBufferSize = 50000
		workerMultiplier = 2
		decodeWindowSize = 16
	)

	enc, err := reedsolomon.New(dataShards, parityShards)
	if err != nil {
		t.Fatalf("创建RS解码器失败: %v", err)
	}
	var encMu sync.Mutex

	shardSize := (chunkSize + dataShards - 1) / dataShards
	totalSymbolsPerShard := (shardSize + symbolSize - 1) / symbolSize
	t.Logf("配置: ChunkSize=%d, ShardSize=%d, Symbols/Shard=%d",
		chunkSize, shardSize, totalSymbolsPerShard)

	type Packet struct {
		chunkIdx  uint64
		shardIdx  uint32
		symbolIdx uint32
		data      []byte
	}

	type ChunkState struct {
		mu            sync.Mutex
		shards        [][]byte
		shardReceived []int
		fullShardCnt  int
		completed     bool
		lastUpdate    time.Time
	}

	newChunkState := func() *ChunkState {
		data := make([]byte, totalShards*shardSize)
		shards := make([][]byte, totalShards)
		for i := range shards {
			shards[i] = data[i*shardSize : (i+1)*shardSize]
		}
		return &ChunkState{
			shards:        shards,
			shardReceived: make([]int, totalShards),
		}
	}

	chunks := make(map[uint64]*ChunkState)
	var chunksMutex sync.Mutex

	var totalPackets int64
	var totalReceived int64
	var totalWritten int64
	startTime := time.Now()

	packetChan := make(chan Packet, packetBufferSize)
	readyChunkChan := make(chan uint64, 8192)
	payloadPool := sync.Pool{
		New: func() interface{} {
			return make([]byte, symbolSize)
		},
	}

	decodeChunk := func(chunkIdx uint64, state *ChunkState) {
		inputShards := make([][]byte, totalShards)
		state.mu.Lock()
		for i := 0; i < totalShards; i++ {
			if state.shardReceived[i] == totalSymbolsPerShard {
				inputShards[i] = state.shards[i]
			} else {
				inputShards[i] = nil
			}
		}
		state.mu.Unlock()

		encMu.Lock()
		ok, err := enc.Verify(inputShards)
		if !ok {
			err = enc.Reconstruct(inputShards)
			if err != nil {
				encMu.Unlock()
				t.Logf("Chunk %d 重建失败: %v", chunkIdx, err)
				return
			}
		}

		writeOffset := int64(chunkIdx) * int64(chunkSize)
		currentChunkSize := chunkSize
		if writeOffset+int64(chunkSize) > fileSize {
			currentChunkSize = int(fileSize - writeOffset)
		}
		sectionWriter := &offsetWriter{f: outputFile, off: writeOffset}
		err = enc.Join(sectionWriter, inputShards, currentChunkSize)
		encMu.Unlock()
		if err != nil {
			t.Logf("Chunk %d 写入失败: %v", chunkIdx, err)
			return
		}

		atomic.AddInt64(&totalWritten, int64(currentChunkSize))
		t.Logf("Chunk %d 解码并写入成功", chunkIdx)

		chunksMutex.Lock()
		delete(chunks, chunkIdx)
		chunksMutex.Unlock()
	}

	var decodeWg sync.WaitGroup
	decodeWg.Add(1)
	go func() {
		defer decodeWg.Done()
		batch := make([]uint64, 0, decodeWindowSize)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			sort.Slice(batch, func(i, j int) bool { return batch[i] < batch[j] })
			for _, idx := range batch {
				chunksMutex.Lock()
				state, exists := chunks[idx]
				chunksMutex.Unlock()
				if !exists {
					continue
				}
				decodeChunk(idx, state)
			}
			batch = batch[:0]
		}
		for idx := range readyChunkChan {
			batch = append(batch, idx)
			if len(batch) >= decodeWindowSize {
				flush()
			}
		}
		flush()
	}()

	processPacket := func(pkt Packet) {
		defer payloadPool.Put(pkt.data[:0])

		chunksMutex.Lock()
		state, exists := chunks[pkt.chunkIdx]
		if !exists {
			state = newChunkState()
			chunks[pkt.chunkIdx] = state
		}
		chunksMutex.Unlock()

		state.mu.Lock()
		if state.completed || int(pkt.shardIdx) >= totalShards {
			state.mu.Unlock()
			return
		}

		offset := int(pkt.symbolIdx) * symbolSize
		if offset+len(pkt.data) > len(state.shards[pkt.shardIdx]) {
			state.mu.Unlock()
			return
		}

		copy(state.shards[pkt.shardIdx][offset:], pkt.data)
		if state.shardReceived[pkt.shardIdx] < totalSymbolsPerShard {
			state.shardReceived[pkt.shardIdx]++
			if state.shardReceived[pkt.shardIdx] == totalSymbolsPerShard {
				state.fullShardCnt++
			}
		}
		state.lastUpdate = time.Now()
		shouldDecode := false
		if !state.completed && state.fullShardCnt >= dataShards {
			state.completed = true
			shouldDecode = true
		}
		state.mu.Unlock()

		if shouldDecode {
			readyChunkChan <- pkt.chunkIdx
		}
	}

	workerCount := runtime.NumCPU() * workerMultiplier
	var workerWg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			for pkt := range packetChan {
				processPacket(pkt)
			}
		}()
	}

	buf := make([]byte, indexSize+symbolSize)
	for {
		udpConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			if os.IsTimeout(err) {
				if atomic.LoadInt64(&totalPackets) > 0 {
					t.Log("接收超时，假设传输结束")
					break
				}
				continue
			}
			t.Logf("读取错误: %v", err)
			continue
		}

		if n < indexSize {
			continue
		}

		seq := binary.BigEndian.Uint64(buf[:indexSize])
		chunkIdx := seq >> 48
		shardIdx := uint32((seq >> 32) & 0xFFFF)
		symbolIdx := uint32(seq & 0xFFFFFFFF)
		payloadLen := n - indexSize
		if payloadLen <= 0 {
			continue
		}

		payload := payloadPool.Get().([]byte)
		if cap(payload) < payloadLen {
			payload = make([]byte, payloadLen)
		}
		payload = payload[:payloadLen]
		copy(payload, buf[indexSize:n])

		pkt := Packet{chunkIdx: chunkIdx, shardIdx: shardIdx, symbolIdx: symbolIdx, data: payload}
		currPackets := atomic.AddInt64(&totalPackets, 1)
		totalReceived += int64(payloadLen)

		select {
		case packetChan <- pkt:
		default:
			payloadPool.Put(payload[:0])
			continue
		}

		if currPackets%10000 == 0 {
			written := atomic.LoadInt64(&totalWritten)
			receivedMB := float64(totalReceived) / (1024 * 1024)
			writtenMB := float64(written) / (1024 * 1024)
			t.Logf("接收进度: 已收 %.2f MB, 已写入 %.2f MB, 包数: %d", receivedMB, writtenMB, currPackets)
		}
	}

	close(packetChan)
	workerWg.Wait()
	close(readyChunkChan)
	decodeWg.Wait()

	t.Log("窗口内解码完成，处理剩余未完成分块...")
	chunkIndices := make([]uint64, 0, len(chunks))
	for idx := range chunks {
		chunkIndices = append(chunkIndices, idx)
	}
	sort.Slice(chunkIndices, func(i, j int) bool { return chunkIndices[i] < chunkIndices[j] })
	for _, idx := range chunkIndices {
		decodeChunk(idx, chunks[idx])
	}
	t.Log("全部分块解码写入完成")

	t.Logf("接收完成: %.2f MB(写入), 包数: %d, 耗时: %v",
		float64(atomic.LoadInt64(&totalWritten))/(1024*1024), atomic.LoadInt64(&totalPackets), time.Since(startTime))

	const expectedMD5 = "cd573cfaace07e7949bc0c46028904ff"
	receivedMD5, err := calcFileMD5("pi_v2_received_file.bin")
	if err != nil {
		t.Fatalf("计算接收文件 MD5 失败: %v", err)
	}
	if expectedMD5 != receivedMD5 {
		t.Fatalf("MD5 不匹配: 期望=%s, 收到=%s", expectedMD5, receivedMD5)
	}
	t.Logf("MD5 校验通过: %s", receivedMD5)

	runtime.ReadMemStats(&memStatsEnd)
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	t.Logf("内存分析结果:")
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024*1024))
	t.Logf("峰值内存使用: %v MB", memStatsEnd.HeapAlloc/(1024*1024))
	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
}

func calcFileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
