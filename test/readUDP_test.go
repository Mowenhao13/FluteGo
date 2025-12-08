package test

import (
	utils "FluteGo/pkg/utils"
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	port   = 3400
	destIP = "192.168.1.102"
)

func TestReadFromUDPWithOsWrite1(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("recv_mem_profile.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write initial heap profile: %v", err)
	}

	key := net.JoinHostPort(destIP, fmt.Sprintf("%d", port))
	conn, err := utils.CreateUDPListener(key)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	// 设置更大的接收缓冲区
	if err := conn.SetReadBuffer(64 * 1024 * 1024); err != nil { // 64MB
		t.Logf("Warning: Failed to set read buffer: %v", err)
	}

	file, err := os.Create("tmp/received_file.bin")
	if err != nil {
		panic(err)
	}
	defer file.Close()

	// 使用bufio优化写入性能
	// 预分配文件空间，减少文件系统元数据更新开销
	if err := file.Truncate(int64(1024 * 1024 * 1024 * 1)); err != nil { // 预分配2GB，足够大
		t.Logf("Warning: Failed to truncate file: %v", err)
	}
	writer := bufio.NewWriterSize(file, 4*1024*1024) // 4MB buffer

	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	// 使用sync.Pool复用缓冲区，减少GC压力
	// 使用较大的缓冲区进行批处理，减少通道交互次数
	const batchSize = 64 * 1024 // 64KB batch
	packetPool := sync.Pool{
		New: func() interface{} {
			return make([]byte, 0, batchSize)
		},
	}

	// ========== 使用通道分离接收和写入 ==========
	dataChan := make(chan []byte, 10000) // 队列长度可以减小，因为每个元素更大
	done := make(chan bool)
	var recv int64 = 0
	totalReceived := 0
	targetSize := 1024 * 1024 * 1024

	// 启动写入文件的goroutine
	go func() {
		defer close(done)
		for data := range dataChan {
			if _, err := writer.Write(data); err != nil {
				t.Errorf("文件写入失败: %v", err)
				return
			}
			atomic.AddInt64(&recv, int64(len(data)))

			// 归还缓冲区
			if cap(data) == batchSize {
				packetPool.Put(data[:0])
			}
		}
		writer.Flush()
	}()

	buf := make([]byte, 64*1024)

	// 设置读取超时
	conn.SetReadDeadline(time.Now().Add(120 * time.Second))

	startTime := time.Now()
	lastDataTime := time.Now()
	lastLogTime := time.Now()
	lastLogRecv := 0

	t.Logf("开始接收数据，目标: %d MB", targetSize/(1024*1024))

	// 接收统计
	packetsReceived := 0
	receiveErrors := 0
	droppedPackets := 0
	var currentBatch []byte // 当前正在填充的batch

	for totalReceived < targetSize {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if os.IsTimeout(err) {
				currentRecv := int(atomic.LoadInt64(&recv))
				t.Logf("接收超时，已接收: %d MB, 待写入: %d MB, 丢包: %d",
					currentRecv/(1024*1024),
					(totalReceived-currentRecv)/(1024*1024),
					droppedPackets)

				// 检查是否真的完成
				if currentRecv == lastLogRecv && len(dataChan) == 0 {
					t.Logf("连续超时且无新数据，传输可能已完成")
					break
				}
				lastLogRecv = currentRecv

				conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				continue
			}
			receiveErrors++
			if receiveErrors > 100 {
				t.Fatalf("Too many receive errors: %v", err)
			}
			continue
		}

		// 成功收到数据
		lastDataTime = time.Now()
		packetsReceived++
		totalReceived += n

		// 批处理逻辑：累积数据到当前batch
		if currentBatch == nil {
			currentBatch = packetPool.Get().([]byte)
		}

		currentBatch = append(currentBatch, buf[:n]...)

		// 如果batch满了，发送到通道
		if len(currentBatch) >= batchSize-2000 { // 留出余量
			select {
			case dataChan <- currentBatch:
				currentBatch = nil // 重置当前batch
			default:
				// 通道满，丢弃整个batch
				droppedPackets += len(currentBatch) / 1400 // 估算包数
				if droppedPackets%5000 == 0 {
					t.Logf("警告: 写入队列满，已丢弃数据")
				}
				// 归还缓冲区
				packetPool.Put(currentBatch[:0])
				currentBatch = nil
			}
		}

		// 重置超时时间

		// 重置超时时间
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		// 进度日志
		currentTime := time.Now()
		currentRecv := int(atomic.LoadInt64(&recv))
		if currentTime.Sub(lastLogTime) > 2*time.Second {
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)
			elapsed := currentTime.Sub(startTime).Seconds()
			receiveRate := float64(totalReceived) / elapsed / (1024 * 1024)
			writeRate := float64(currentRecv) / elapsed / (1024 * 1024)

			t.Logf("状态: 收包%dMB(%.1fMB/s) 写入%dMB(%.1fMB/s) 队列:%d 内存:%vMB",
				totalReceived/(1024*1024), receiveRate,
				currentRecv/(1024*1024), writeRate,
				len(dataChan),
				memStats.Alloc/(1024*1024))

			lastLogTime = currentTime
		}

		// 超时检查
		if time.Since(lastDataTime) > 30*time.Second {
			t.Logf("30秒内未收到新数据，传输可能已完成")
			break
		}
	}

	// 发送剩余的batch
	if len(currentBatch) > 0 {
		dataChan <- currentBatch
	}

	// 关闭数据通道，等待写入完成
	close(dataChan)
	<-done

	// 最终文件同步
	if err := file.Sync(); err != nil {
		t.Logf("最终文件同步失败: %v", err)
	}

	finalReceived := int(atomic.LoadInt64(&recv))
	totalTime := time.Since(startTime)

	t.Logf("=== 接收完成 ===")
	t.Logf("网络接收: %d bytes (%d MB)", totalReceived, totalReceived/(1024*1024))
	t.Logf("实际写入: %d bytes (%d MB)", finalReceived, finalReceived/(1024*1024))
	t.Logf("总包数: %d", packetsReceived)
	t.Logf("总耗时: %v", totalTime)
	t.Logf("平均速率: %.2f MB/s", float64(finalReceived)/(1024*1024)/totalTime.Seconds())

	// 最终文件同步
	if err := file.Sync(); err != nil {
		t.Errorf("最终文件同步失败: %v", err)
	}

	// 记录结束时的内存状态
	runtime.ReadMemStats(&memStatsEnd)

	// 写入最终的内存profile
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 输出详细分析
	t.Logf("性能分析:")
	t.Logf("接收/写入差异: %d bytes", totalReceived-finalReceived)
	t.Logf("丢包率: %.2f%%", float64(totalReceived-finalReceived)*100/float64(totalReceived))
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024*1024))
	t.Logf("峰值内存使用: %v MB", memStatsEnd.HeapAlloc/(1024*1024))

	// 验证文件大小
	// 注意：由于使用了Truncate预分配，file.Stat().Size()会返回预分配的大小
	// 我们应该截断文件到实际写入的大小
	if err := file.Truncate(int64(finalReceived)); err != nil {
		t.Errorf("截断文件失败: %v", err)
	}

	fileInfo, err := file.Stat()
	if err != nil {
		t.Errorf("无法获取文件信息: %v", err)
	} else {
		actualSize := fileInfo.Size()
		t.Logf("实际文件大小: %d MB", actualSize/(1024*1024))

		if int64(finalReceived) != actualSize {
			t.Errorf("接收数据大小(%d)与文件大小(%d)不一致!", finalReceived, actualSize)
		}
	}

	// 完成统计
	if finalReceived < targetSize {
		t.Logf("完成度: %d/%d MB (%.1f%%)",
			finalReceived/(1024*1024), targetSize/(1024*1024),
			float64(finalReceived)*100/float64(targetSize))
	} else {
		t.Logf("成功: 完成目标大小接收")
	}
}

func TestReadFromUDPWithOsWriteOrdered(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("recv_mem_profile.pprof")
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
		t.Fatalf("Failed to listen UDP: %v", err)
	}
	defer conn.Close()

	udpConn := conn.(*net.UDPConn)
	t.Logf("开始监听UDP端口: %d", port)

	// 确保目录存在
	if err := os.MkdirAll("tmp", 0755); err != nil {
		t.Fatalf("Failed to create tmp directory: %v", err)
	}

	file, err := os.Create("tmp/received_file.bin")
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}
	defer file.Close()

	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	// 配置参数
	const (
		indexSize     = 8
		maxPacketSize = 1400
		targetSize    = 1073741824 // 1GB
	)

	// 改进的数据结构：使用环形缓冲区避免内存爆炸
	type packetInfo struct {
		data     []byte
		received bool
	}

	// 预估最大包数量，创建固定大小的缓冲区
	estimatedPackets := targetSize/(maxPacketSize-indexSize) + 10000
	packetBuffer := make([]*packetInfo, estimatedPackets)

	expectedSequence := uint64(0)
	totalReceived := 0
	totalPackets := 0
	reorderedPackets := 0
	duplicatePackets := 0
	maxReceivedSeq := uint64(0)
	minReceivedSeq := uint64(0)

	buf := make([]byte, maxPacketSize)

	// 设置更大的接收缓冲区
	udpConn.SetReadBuffer(128 * 1024 * 1024)

	// 设置初始读取超时
	startTime := time.Now()
	lastDataTime := time.Now()
	conn.SetReadDeadline(time.Now().Add(300 * time.Second))

	t.Logf("开始接收数据，目标大小: %d MB", targetSize/(1024*1024))

	// 创建定期刷新文件的ticker
	flushTicker := time.NewTicker(500 * time.Millisecond) // 降低同步频率
	defer flushTicker.Stop()

	// 统计信息打印ticker
	statsTicker := time.NewTicker(2 * time.Second)
	defer statsTicker.Stop()

	for totalReceived < targetSize {
		select {
		case <-flushTicker.C:
			// 定期刷新文件
			if err := file.Sync(); err != nil {
				t.Logf("文件同步失败: %v", err)
			}
		case <-statsTicker.C:
			// 定期打印统计信息
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)
			elapsed := time.Since(startTime).Seconds()
			rate := float64(totalReceived) / elapsed / (1024 * 1024)

			// 计算缓存包数量
			cachedCount := 0
			for i := expectedSequence; i <= maxReceivedSeq && i < uint64(len(packetBuffer)); i++ {
				if packetBuffer[i] != nil && !packetBuffer[i].received {
					cachedCount++
				}
			}

			t.Logf("状态: 接收%dMB(%.2fMB/s), 包:%d, 乱序:%d, 缓存:%d, 期望seq:%d, 最大seq:%d",
				totalReceived/(1024*1024), rate, totalPackets, reorderedPackets,
				cachedCount, expectedSequence, maxReceivedSeq)
		default:
			// 继续接收数据
		}

		n, addr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				t.Logf("接收超时，已接收: %d MB", totalReceived/(1024*1024))
				break
			}
			t.Fatalf("Failed to read data from UDP: %v", err)
		}

		totalPackets++
		lastDataTime = time.Now()

		if n < indexSize {
			t.Logf("包大小过小: %d bytes, 来自: %s", n, addr)
			continue
		}

		// 解析序列号
		sequence := binary.BigEndian.Uint64(buf[0:indexSize])
		packetData := buf[indexSize:n]

		// 更新最大最小序列号
		if sequence > maxReceivedSeq || maxReceivedSeq == 0 {
			maxReceivedSeq = sequence
		}
		if sequence < minReceivedSeq || minReceivedSeq == 0 {
			minReceivedSeq = sequence
		}

		// 检查序列号是否在合理范围内
		if sequence >= uint64(len(packetBuffer)) {
			t.Logf("序列号超出范围: %d, 忽略此包", sequence)
			continue
		}

		// 处理包数据
		if packetBuffer[sequence] == nil {
			// 新包，存储数据
			copiedData := make([]byte, len(packetData))
			copy(copiedData, packetData)
			packetBuffer[sequence] = &packetInfo{
				data:     copiedData,
				received: false,
			}
		} else {
			// 重复包
			duplicatePackets++
			if duplicatePackets%10000 == 0 {
				t.Logf("重复包: seq=%d, 总重复数=%d", sequence, duplicatePackets)
			}
			continue
		}

		// 尝试按顺序写入文件
		for packetBuffer[expectedSequence] != nil && !packetBuffer[expectedSequence].received {
			data := packetBuffer[expectedSequence].data
			if _, err := file.Write(data); err != nil {
				t.Fatalf("文件写入失败: %v", err)
			}
			packetBuffer[expectedSequence].received = true
			totalReceived += len(data)
			expectedSequence++

			// 每写入一定数据后检查是否可以释放内存
			if expectedSequence%1000 == 0 && expectedSequence > 1000 {
				// 释放已经处理过的包的内存
				for i := expectedSequence - 1000; i < expectedSequence-100; i++ {
					if i < uint64(len(packetBuffer)) {
						packetBuffer[i] = nil
					}
				}
			}
		}

		// 重置超时时间
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))

		// 超时检查
		if time.Since(lastDataTime) > 30*time.Second {
			t.Logf("30秒内未收到新数据，传输可能已完成")
			break
		}
	}

	// 最终尝试处理所有剩余包
	t.Logf("最终处理，期望序列号: %d, 最大序列号: %d", expectedSequence, maxReceivedSeq)
	for i := expectedSequence; i <= maxReceivedSeq && i < uint64(len(packetBuffer)); i++ {
		if packetBuffer[i] != nil && !packetBuffer[i].received {
			if _, err := file.Write(packetBuffer[i].data); err != nil {
				t.Errorf("最终写入失败 seq=%d: %v", i, err)
			} else {
				totalReceived += len(packetBuffer[i].data)
				packetBuffer[i].received = true
				reorderedPackets++
			}
		}
	}

	// 最终文件同步
	if err := file.Sync(); err != nil {
		t.Errorf("最终文件同步失败: %v", err)
	}

	t.Logf("=== 顺序接收完成 ===")
	t.Logf("总接收数据: %d bytes (%d MB)", totalReceived, totalReceived/(1024*1024))
	t.Logf("总数据包数: %d", totalPackets)
	t.Logf("乱序重组包数: %d", reorderedPackets)
	t.Logf("重复包数: %d", duplicatePackets)

	// 计算未处理的包
	remaining := 0
	for i := expectedSequence; i <= maxReceivedSeq && i < uint64(len(packetBuffer)); i++ {
		if packetBuffer[i] != nil && !packetBuffer[i].received {
			remaining++
		}
	}
	t.Logf("剩余未处理包数: %d", remaining)
	t.Logf("总耗时: %v", time.Since(startTime))

	// 记录结束时的内存状态
	runtime.ReadMemStats(&memStatsEnd)

	// 写入最终的内存profile
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 验证文件大小
	fileInfo, err := file.Stat()
	if err != nil {
		t.Errorf("无法获取文件信息: %v", err)
	} else {
		actualSize := fileInfo.Size()
		t.Logf("实际文件大小: %d bytes (%d MB)", actualSize, actualSize/(1024*1024))

		if int64(totalReceived) != actualSize {
			t.Errorf("接收数据大小(%d)与文件大小(%d)不一致!", totalReceived, actualSize)
		}
	}

	if remaining > 0 {
		t.Errorf("仍有 %d 个包未处理，期望seq: %d, 最大收到seq: %d",
			remaining, expectedSequence, maxReceivedSeq)
	}
}

func TestReadFromUDPWithOsWritesOrderedGoroutines(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("recv_mem_profile.pprof")
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

	file, err := os.Create("received_file.bin")
	if err != nil {
		panic(err)
	}
	defer file.Close()

	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	// 用于顺序重组的数据结构
	const indexSize = 8
	const maxPacketSize = 1400

	// 使用细粒度的线程安全数据结构
	type sharedState struct {
		packetMap        sync.Map
		expectedSequence uint64 // 需要顺序更新，用锁保护
		totalReceived    int64  // 使用原子操作
		totalPackets     int64  // 使用原子操作
		reorderedPackets int64  // 使用原子操作
		lastDataTime     int64  // 使用原子时间戳(UnixNano)
		file             *os.File
		fileMutex        sync.Mutex // 文件写入需要单独的锁
		sequenceMutex    sync.Mutex // 保护expectedSequence和相关操作
		startTime        time.Time
		cacheCount       int32 // 用于原子操作记录缓存包数量
	}

	state := &sharedState{
		packetMap:        sync.Map{},
		expectedSequence: 0,
		totalReceived:    0,
		totalPackets:     0,
		reorderedPackets: 0,
		lastDataTime:     time.Now().UnixNano(),
		file:             file,
		startTime:        time.Now(),
		cacheCount:       0,
	}

	targetSize := int64(1073741824) // 1GB
	udpConn.SetReadBuffer(128 * 1024 * 1024)
	conn.SetReadDeadline(time.Now().Add(300 * time.Second))

	// 使用sync.WaitGroup等待所有goroutine完成
	var wg sync.WaitGroup
	workerCount := 1

	t.Logf("启动 %d 个goroutine进行并发接收", workerCount)

	// 创建context用于协调goroutine退出
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := 0
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			buf := make([]byte, maxPacketSize)

			for {
				select {
				case <-ctx.Done():
					t.Logf("Worker %d 被取消", workerID)
					return
				default:
				}

				// 检查是否已完成目标
				if atomic.LoadInt64(&state.totalReceived) >= targetSize {
					break
				}

				// 设置读取超时
				conn.SetReadDeadline(time.Now().Add(1 * time.Second))

				n, addr, err := udpConn.ReadFromUDP(buf)
				if err != nil {
					if os.IsTimeout(err) {
						// 检查是否真的超时（没有数据）还是已完成
						lastTime := time.Unix(0, atomic.LoadInt64(&state.lastDataTime))
						idleTime := time.Since(lastTime)
						totalReceived := atomic.LoadInt64(&state.totalReceived)

						if idleTime > 30*time.Second || totalReceived >= targetSize {
							t.Logf("Worker %d 超时退出，空闲: %v", workerID, idleTime)
							return
						}
						continue
					}
					t.Logf("Worker %d 读取错误: %v", workerID, err)
					continue
				}

				if n < indexSize {
					t.Logf("Worker %d: 包大小过小: %d bytes, 来自: %s", workerID, n, addr)
					continue
				}

				// 解析序列号和数据
				sequence := binary.BigEndian.Uint64(buf[0:indexSize])
				packetData := make([]byte, n-indexSize) // 复制数据，避免竞争
				copy(packetData, buf[indexSize:n])
				received += len(packetData)
				// 更新原子计数器
				atomic.AddInt64(&state.totalPackets, 1)
				atomic.StoreInt64(&state.lastDataTime, time.Now().UnixNano())

				totalPackets := atomic.LoadInt64(&state.totalPackets)

				if received%(10*1024) == 0 {
					t.Logf("Worker %d: 收到包: seq=%d, size=%d, 总包数=%d",
						workerID, sequence, len(packetData), totalPackets)
				}

				// 处理包顺序（需要加锁保证原子性）
				state.sequenceMutex.Lock()

				if sequence == state.expectedSequence {
					// 按顺序的包，直接写入
					state.fileMutex.Lock()
					if _, err := state.file.Write(packetData); err != nil {
						state.fileMutex.Unlock()
						state.sequenceMutex.Unlock()
						panic(err)
					}
					state.fileMutex.Unlock()

					atomic.AddInt64(&state.totalReceived, int64(len(packetData)))
					state.expectedSequence++

					// 检查是否有缓存的后续包
					for {
						if cachedData, exists := state.packetMap.Load(state.expectedSequence); exists {
							state.fileMutex.Lock()
							if _, err := state.file.Write(cachedData.([]byte)); err != nil {
								state.fileMutex.Unlock()
								state.sequenceMutex.Unlock()
								panic(err)
							}
							state.fileMutex.Unlock()

							dataLen := len(cachedData.([]byte))
							atomic.AddInt64(&state.totalReceived, int64(dataLen))
							atomic.AddInt64(&state.reorderedPackets, 1)
							state.packetMap.Delete(state.expectedSequence)
							atomic.AddInt32(&state.cacheCount, -1)
							state.expectedSequence++
						} else {
							break
						}
					}
				} else if sequence > state.expectedSequence {
					// 乱序的包，先缓存起来
					state.packetMap.Store(sequence, packetData)
					atomic.AddInt32(&state.cacheCount, 1)
				} else {
					// 重复的包，忽略
					if totalPackets%1000 == 0 {
						t.Logf("收到重复包: sequence=%d, expected=%d",
							sequence, state.expectedSequence)
					}
				}
				state.sequenceMutex.Unlock()

				// 记录状态
				currentReceived := atomic.LoadInt64(&state.totalReceived)
				currentCacheCount := atomic.LoadInt32(&state.cacheCount)
				if currentReceived%(1*1024*1024) == 0 && currentReceived > 0 {
					var memStats runtime.MemStats
					runtime.ReadMemStats(&memStats)
					elapsed := time.Since(state.startTime).Seconds()
					rate := float64(currentReceived) / elapsed / (1024 * 1024)
					t.Logf("已接收: %d MB, 速率: %.2f MB/s, 包数: %d, 乱序包: %d, 缓存包: %d, 内存: %v MB",
						currentReceived/(1024*1024), rate, atomic.LoadInt64(&state.totalPackets),
						atomic.LoadInt64(&state.reorderedPackets), currentCacheCount,
						memStats.Alloc/(1024*1024))
				}

				// 定期清理过期的缓存包（在锁外执行，避免长时间持锁）
				if currentCacheCount > 1000 {
					cleaned := 0
					state.sequenceMutex.Lock()
					expectedSeq := state.expectedSequence
					state.sequenceMutex.Unlock()

					state.packetMap.Range(func(key, value interface{}) bool {
						seq := key.(uint64)
						if seq < expectedSeq-1000 {
							state.packetMap.Delete(seq)
							atomic.AddInt32(&state.cacheCount, -1)
							cleaned++
						}
						return true
					})
					if cleaned > 0 {
						t.Logf("清理了 %d 个过期缓存包", cleaned)
					}
				}

				// 检查是否完成
				if currentReceived >= targetSize {
					break
				}
			}
			t.Logf("Worker %d 退出", workerID)
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
	finalCacheCount := 0
	state.packetMap.Range(func(key, value interface{}) bool {
		finalCacheCount++
		return true
	})

	t.Logf("=== 顺序接收完成 ===")
	totalReceived := atomic.LoadInt64(&state.totalReceived)
	totalPackets := atomic.LoadInt64(&state.totalPackets)
	reorderedPackets := atomic.LoadInt64(&state.reorderedPackets)

	t.Logf("总接收数据: %d bytes (%d MB)", totalReceived, totalReceived/(1024*1024))
	t.Logf("总数据包数: %d", totalPackets)
	t.Logf("乱序重组包数: %d", reorderedPackets)
	t.Logf("剩余缓存包数: %d", finalCacheCount)
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

	// 验证文件大小
	fileInfo, err := file.Stat()
	if err != nil {
		t.Errorf("无法获取文件信息: %v", err)
	} else {
		t.Logf("实际文件大小: %d bytes (%d MB)", fileInfo.Size(), fileInfo.Size()/(1024*1024))
	}
}
