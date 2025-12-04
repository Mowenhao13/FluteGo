package test

import (
	pool "FluteGo/pkg/pool"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/time/rate"
)

const (
	kb        = 1024
	mb        = 1024 * 1024
	chunkSize = 10240
	sendPort  = 3401
)

func init() {
	go func() {
		pool.InitGlobalConnectionPool(100, 120*time.Second, 0)
	}()
	time.Sleep(100 * time.Millisecond)
}

func openFileMmap() (os.FileInfo, []byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to open file: %v\n", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to get file info: %v\n", err)
	}

	fd := int(file.Fd())
	fileSize := info.Size()
	data, err := unix.Mmap(fd, 0, int(fileSize), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to MMAP: %v\n", err)
	}
	// defer unix.Munmap(data)

	return info, data, nil
}

// total file mmap
func TestWriteFromUDPWithMmap(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("send_mem_profile.pprof")
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

	info, data, err := openFileMmap()
	if err != nil {
		panic(err)
	}

	defer unix.Munmap(data)

	fileSize := info.Size()
	fmt.Printf("文件大小: %d bytes\n", fileSize)
	// 记录发送过程中的内存分配
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	sent := 0
	// Block conn
	for i := 0; i <= int(fileSize); i += chunkSize {
		end := i + chunkSize
		if end > int(fileSize) {
			end = int(fileSize)
		}

		actualWrite, err := udpConn.Conn.Write(data[i:end])
		if err != nil {
			panic(err)
		}

		sent += actualWrite

		if sent%(50 * 1024) == 0 { // 每10MB记录一次内存状态
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)
			t.Logf("已发送: %d MB, 内存分配: %v MB", 
				sent/(1024 * 1024), 
				memStats.Alloc/(1024 * 1024))
			time.Sleep(1 * time.Millisecond)
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
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024 * 1024))
	t.Logf("峰值内存使用: %v MB", memStatsEnd.HeapAlloc/(1024 * 1024))
	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)

}

func TestWriteFromUDPWithMmapChunked(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("send_mmap_chunked_mem_profile.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
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

	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		t.Fatalf("Failed to get file info: %v", err)
	}
	fileSize := info.Size()
	t.Logf("文件大小: %d bytes (%d MB)", fileSize, fileSize/(1024 * 1024))

	fd := int(file.Fd())
	chunkSz := 32 * 1024 * 1024 // 32MB 分段大小
	sent := 0
	totalPackets := 0

	// 记录开始内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	startTime := time.Now()

	// ========== 拥塞控制配置 ==========
	limitMbps := 1000.0 // 速率限制，单位Mbps（可根据需要调整）
	bytesPerSec := int(limitMbps * 1_000_000 / 8)
	burst := bytesPerSec  // burst大小为每秒字节数
	
	t.Logf("拥塞控制配置: 限制速率=%.0f Mbps, 每秒字节数=%d, burst大小=%d", 
		limitMbps, bytesPerSec, burst)

	rateLimiter := rate.NewLimiter(rate.Limit(bytesPerSec), burst)

	// 设置UDP发送缓冲区
	if err := udpConn.Conn.SetWriteBuffer(2 * 1024 * 1024); err != nil {
		t.Logf("Warning: Failed to set write buffer: %v", err)
	}

	// 统计变量
	lastLogTime := time.Now()
	lastLogSent := 0

	for offset := int64(0); offset < fileSize; offset += int64(chunkSz) {
		// 计算当前chunk的实际大小
		remaining := fileSize - offset
		mapSize := chunkSz
		if remaining < int64(chunkSz) {
			mapSize = int(remaining)
		}

		t.Logf("映射 chunk: 偏移量=%d, 大小=%d bytes", offset, mapSize)

		// 映射当前chunk
		data, err := unix.Mmap(fd, offset, mapSize, unix.PROT_READ, unix.MAP_SHARED)
		if err != nil {
			t.Fatalf("MMAP failed at offset %d: %v", offset, err)
		}

		// 分块发送当前映射的数据
		chunkSent := 0
		chunkPackets := 0
		
		for i := 0; i < len(data); {
			// 计算当前包大小（最大1400字节）
			packetSize := 1400
			if i+packetSize > len(data) {
				packetSize = len(data) - i
			}

			chunk := data[i : i+packetSize]
			
			// ========== 应用速率限制 ==========
			if err := rateLimiter.WaitN(context.Background(), len(chunk)); err != nil {
				unix.Munmap(data)
				t.Fatalf("Rate limiter error: %v", err)
			}

			// 发送数据包
			actualWrite, err := udpConn.Conn.Write(chunk)
			if err != nil {
				unix.Munmap(data)
				t.Fatalf("发送失败 at chunk offset %d: %v", i, err)
			}

			sent += actualWrite
			chunkSent += actualWrite
			totalPackets++
			chunkPackets++
			i += packetSize

			// ========== 进度日志和统计 ==========
			if time.Since(lastLogTime) > 2*time.Second {
				var memStats runtime.MemStats
				runtime.ReadMemStats(&memStats)
				elapsed := time.Since(startTime).Seconds()
				currentRate := float64(sent-lastLogSent) / (1024 * 1024) / time.Since(lastLogTime).Seconds()
				avgRate := float64(sent) / elapsed / (1024 * 1024)
				
				t.Logf("进度: 已发送%dMB, 当前速率: %.2f MB/s, 平均速率: %.2f MB/s, 包数: %d, 内存: %v MB", 
					sent/(1024 * 1024), currentRate, avgRate, totalPackets, memStats.Alloc/(1024 * 1024))
				
				lastLogTime = time.Now()
				lastLogSent = sent
			}

			// ========== 添加微小延迟避免拥塞 ==========
			if chunkPackets%50 == 0 { // 每50个包延迟一次
				time.Sleep(100 * time.Microsecond)
			}
		}

		t.Logf("Chunk完成: 偏移量=%d, 发送=%d bytes, 包数=%d", offset, chunkSent, chunkPackets)

		// 取消映射当前chunk
		if err := unix.Munmap(data); err != nil {
			t.Fatalf("Munmap failed at offset %d: %v", offset, err)
		}

		// 记录每个chunk完成后的内存状态
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		t.Logf("Chunk %d 完成, 当前内存: %v MB", offset/int64(chunkSz), memStats.HeapAlloc/(1024 * 1024))

		// ========== Chunk之间的延迟 ==========
		if offset+int64(chunkSz) < fileSize { // 不是最后一个chunk
			time.Sleep(10 * time.Millisecond) // 给系统一些喘息时间
		}
	}

	// 记录结束内存状态
	runtime.ReadMemStats(&memStatsEnd)
	totalTime := time.Since(startTime)

	// 写入最终内存profile
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 输出详细结果
	t.Logf("=== 分块MMAP发送完成 ===")
	t.Logf("总发送数据: %d bytes (%d MB)", sent, sent/(1024 * 1024))
	t.Logf("总包数: %d", totalPackets)
	t.Logf("总耗时: %v", totalTime)
	t.Logf("平均速率: %.2f MB/s", float64(sent)/(1024 * 1024)/totalTime.Seconds())
	
	// 计算实际使用的带宽
	actualMbps := float64(sent) * 8 / totalTime.Seconds() / 1_000_000
	t.Logf("实际带宽使用: %.2f Mbps", actualMbps)
	
	t.Logf("内存分析:")
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024 * 1024))
	t.Logf("峰值内存使用: %v MB", memStatsEnd.HeapAlloc/(1024 * 1024))
	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)

	// 验证完整性
	if int64(sent) != fileSize {
		t.Logf("警告: 发送数据量(%d)与文件大小(%d)不匹配", sent, fileSize)
	} else {
		t.Logf("数据完整性: 发送数据量与文件大小匹配")
	}
}

func TestWriteFromUDPWithReadFile(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("send_readfile_mem_profile.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
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

	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	t.Logf("开始读取文件到内存...")
	
	// 使用os.ReadFile读取整个文件到内存
	startRead := time.Now()
	fileData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	readDuration := time.Since(startRead)

	fileSize := len(fileData)
	t.Logf("文件读取完成: %d bytes (%d MB), 耗时: %v", fileSize, fileSize/(1024 * 1024), readDuration)
	t.Logf("切片长度: %d MB, 切片容量: %d MB", len(fileData)/(1024 * 1024), cap(fileData)/(1024 * 1024))

	// 记录读取文件后的内存状态
	var memStatsAfterRead runtime.MemStats
	runtime.ReadMemStats(&memStatsAfterRead)
	t.Logf("读取文件后内存占用: %v MB", memStatsAfterRead.HeapAlloc/(1024 * 1024))

	sent := 0
	totalPackets := 0
	startSend := time.Now()

	// 发送数据
	for i := 0; i < fileSize; i += chunkSize {
		end := i + chunkSize
		if end > fileSize {
			end = fileSize
		}

		chunk := fileData[i:end]
		actualWrite, err := udpConn.Conn.Write(chunk)
		if err != nil {
			panic(err)
		}

		sent += actualWrite
		totalPackets++

		if sent%(50 * 1024) == 0 { // 每100KB记录一次状态
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)
			t.Logf("已发送: %d MB, 内存使用: %v MB, 数据包: %d", 
				sent/(1024 * 1024), 
				memStats.HeapAlloc/(1024 * 1024),
				totalPackets)
			time.Sleep(1 * time.Millisecond)
		}
	}

	sendDuration := time.Since(startSend)
	t.Logf("数据发送完成，总耗时: %v", sendDuration)

	// 记录结束时的内存状态
	runtime.ReadMemStats(&memStatsEnd)

	// 清理文件数据，帮助GC
	fileData = nil
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	
	var memStatsAfterGC runtime.MemStats
	runtime.ReadMemStats(&memStatsAfterGC)

	// 写入最终的内存profile
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 输出详细的内存分析结果
	t.Logf("=== 内存性能分析结果 ===")
	t.Logf("文件大小: %d MB", fileSize/(1024 * 1024))
	t.Logf("总发送数据: %d MB", sent/(1024 * 1024))
	t.Logf("总数据包数: %d", totalPackets)
	t.Logf("平均发送速率: %.2f MB/s", float64(sent)/(1024 * 1024)/sendDuration.Seconds())
	t.Logf("读取文件耗时: %v", readDuration)
	t.Logf("发送数据耗时: %v", sendDuration)
	
	t.Logf("内存使用情况:")
	t.Logf("初始内存: %v MB", memStatsStart.HeapAlloc/(1024 * 1024))
	t.Logf("读取文件后内存: %v MB", memStatsAfterRead.HeapAlloc/(1024 * 1024))
	t.Logf("发送过程中峰值内存: %v MB", memStatsEnd.HeapAlloc/(1024 * 1024))
	t.Logf("清理数据GC后内存: %v MB", memStatsAfterGC.HeapAlloc/(1024 * 1024))
	
	t.Logf("内存分配统计:")
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024 * 1024))
	t.Logf("堆内存增量: %v MB", (memStatsEnd.HeapAlloc-memStatsStart.HeapAlloc)/(1024 * 1024))
	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
	t.Logf("内存分配次数: %v", memStatsEnd.Mallocs-memStatsStart.Mallocs)
	t.Logf("内存释放次数: %v", memStatsEnd.Frees-memStatsStart.Frees)

	// 计算内存使用效率
	memoryEfficiency := float64(fileSize) / float64(memStatsAfterRead.HeapAlloc-memStatsStart.HeapAlloc)
	t.Logf("内存使用效率: %.2f%%", memoryEfficiency*100)

	// 验证发送完整性
	if sent != fileSize {
		t.Logf("警告: 发送数据量(%d)与文件大小(%d)不匹配", sent, fileSize)
	} else {
		t.Logf("数据完整性: 发送数据量与文件大小匹配")
	}
}

func TestWriteFromUDPWithMmapOrdered(t *testing.T) {
	// 创建内存profile文件
	memProfile, err := os.Create("send_mem_profile.pprof")
	if err != nil {
		t.Fatalf("Failed to create memory profile: %v", err)
	}
	defer memProfile.Close()

	// 在测试开始时获取内存快照
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

	info, data, err := openFileMmap()
	if err != nil {
		panic(err)
	}
	defer unix.Munmap(data)

	fileSize := info.Size()
	fmt.Printf("文件大小: %d bytes\n", fileSize)
	
	// 记录发送过程中的内存分配
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	// 定义数据包结构：8字节索引 + 数据
	const indexSize = 8
	const maxDataSize = 1400 - indexSize
	
	sequence := uint64(0)
	sent := 0
	totalPackets := 0
	startTime := time.Now()

	// 重用缓冲区，避免重复分配
	packetBuf := make([]byte, 1400) // 固定大小的缓冲区

	limitMbps := 100.0 // 速率限制，单位Mbps
	bytesPerSec := int(limitMbps * 1_000_000 / 8)
	burst := bytesPerSec 
	if burst <= 0 {
		burst = bytesPerSec
	}

	rateLimiter := rate.NewLimiter(rate.Limit(float64(bytesPerSec)), burst)
	// 设置UDP连接选项，优化发送性能
	conn := udpConn.Conn
	// 增大发送缓冲区
	if err := conn.SetWriteBuffer(20 * 1024 * 1024); err != nil {
		t.Logf("Warning: Failed to set write buffer: %v", err)
	}
	

	// 发送数据
	lastLogTime := time.Now()
	lastLogSent := 0
	

	// 发送数据
	for i := 0; i < int(fileSize); i += maxDataSize {
		end := i + maxDataSize
		if end > int(fileSize) {
			end = int(fileSize)
		}

		dataSize := end - i
		
		// 重用缓冲区，避免重复分配
		// 写入8字节的序列号（大端序）
		binary.BigEndian.PutUint64(packetBuf[0:indexSize], sequence)
		
		// 写入实际数据
		copy(packetBuf[indexSize:indexSize+dataSize], data[i:end])
		
		// 发送数据包
		if err := rateLimiter.WaitN(context.Background(), indexSize+dataSize); err != nil {
			t.Fatalf("Rate limiter error: %v", err)
		}

		// 发送数据包
		_, err := udpConn.Conn.Write(packetBuf[:indexSize+dataSize])
		if err != nil {
			// 如果是暂时性错误，重试一次
			if neterr, ok := err.(net.Error); ok && neterr.Temporary() {
				time.Sleep(10 * time.Millisecond)
				_, err = udpConn.Conn.Write(packetBuf[:indexSize+dataSize])
			}
			if err != nil {
				t.Fatalf("Failed to send packet %d: %v", sequence, err)
			}
		}

		sent += dataSize
		sequence++
		totalPackets++

		// 更频繁但更轻量的日志
		if time.Since(lastLogTime) > 1*time.Second {
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)
			elapsed := time.Since(startTime).Seconds()
			currentRate := float64(sent-lastLogSent) / (1024 * 1024) / time.Since(lastLogTime).Seconds()
			avgRate := float64(sent) / elapsed / (1024 * 1024)
			
			t.Logf("已发送: %d MB, 当前速率: %.2f MB/s, 平均速率: %.2f MB/s, 包数: %d", 
				sent/(1024 * 1024), currentRate, avgRate, totalPackets)
			
			lastLogTime = time.Now()
			lastLogSent = sent
		}

		// 添加微小延迟，避免网络拥塞
		if totalPackets%100 == 0 {
			time.Sleep(10 * time.Microsecond)
		}
	}

	// // 发送结束标记（可选）
	// endPacket := make([]byte, indexSize)
	// binary.BigEndian.PutUint64(endPacket, sequence) // 使用下一个序列号作为结束标记
	// udpConn.Conn.Write(endPacket)
	// t.Logf("发送结束标记，序列号: %d", sequence)

	runtime.ReadMemStats(&memStatsEnd)
	totalTime := time.Since(startTime)

	// 写入最终的内存profile
	runtime.GC()
	if err := pprof.WriteHeapProfile(memProfile); err != nil {
		t.Fatalf("Failed to write final heap profile: %v", err)
	}

	// 输出内存分析结果
	t.Logf("=== 顺序发送完成 ===")
	t.Logf("总发送数据: %d bytes (%d MB)", sent, sent/(1024 * 1024))
	t.Logf("总数据包数: %d", totalPackets)
	t.Logf("总耗时: %v", totalTime)
	t.Logf("平均速率: %.2f MB/s", float64(sent)/(1024 * 1024)/totalTime.Seconds())
	t.Logf("内存分析结果:")
	t.Logf("总分配内存: %v MB", (memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)/(1024 * 1024))
	t.Logf("峰值内存使用: %v MB", memStatsEnd.HeapAlloc/(1024 * 1024))
	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
}

