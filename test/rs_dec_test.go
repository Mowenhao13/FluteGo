package test

import (
	"FluteGo/pkg/utils"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	rs "github.com/klauspost/reedsolomon"
)

var (
	outFile  = "/home/Halllo/Projects/Flute_test_v2/cmd/received_files/test_1024mb.bin"
	fileSize = 1024 * 1024 * 1024
	fnameW   = "/home/Halllo/Projects/Flute_test_v2/test/tmp_recv/test_1024mb.bin"
	// dataShards   = 4
	// parityShards = 2
	// symbolSize   = 1400 // 确保与发送端一致
)

func openInput(dataShards, parityShards int, fname string) ([]io.Reader, int64, error) {
	totalShards := dataShards + parityShards
	shards := make([]io.Reader, totalShards)
	var maxSize int64

	for i := range shards {
		infn := fmt.Sprintf("%s.%d", fname, i)
		f, err := os.Open(infn)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("  Missing shard: %s\n", infn)
				shards[i] = nil
				continue
			}
			return nil, 0, err
		}

		stat, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, err
		}

		if stat.Size() > 0 {
			shards[i] = f
			if stat.Size() > maxSize {
				maxSize = stat.Size()
			}
			fmt.Printf("  ✓ %s (%d bytes)\n", infn, stat.Size())
		} else {
			fmt.Printf("  ✗ Empty shard: %s\n", infn)
			shards[i] = nil
			f.Close()
		}
	}

	return shards, maxSize, nil
}

func decode(fname string) error {
	// 确保输出目录存在
	outDir := filepath.Dir(outFile)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	enc, err := rs.NewStream(dataShards, parityShards)
	if err != nil {
		return fmt.Errorf("create encoder: %w", err)
	}

	log.Println("Opening input shards...")
	shards, sz, err := openInput(dataShards, parityShards, fname)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer func() {
		for _, s := range shards {
			if c, ok := s.(io.Closer); ok && s != nil {
				c.Close()
			}
		}
	}()

	log.Println("Verifying shards...")
	ok, err := enc.Verify(shards)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	// enc.Verify may have read from the shard readers; reset their offsets to the start
	for _, s := range shards {
		if s == nil {
			continue
		}
		if seeker, ok := s.(io.Seeker); ok {
			seeker.Seek(0, 0)
		}
	}

	totalSize := int64(dataShards) * sz
	log.Printf("Total data size: %d bytes (%.2f MB)\n", totalSize, float64(totalSize)/1024/1024)

	if !ok {
		log.Println("✗ Verification failed. Reconstructing data...")
		// 重新打开文件用于重建
		for _, s := range shards {
			if c, ok := s.(io.Closer); ok && s != nil {
				c.Close()
			}
		}

		// 重新打开所有分片
		shards, sz, err = openInput(dataShards, parityShards, fname)
		if err != nil {
			return fmt.Errorf("reopen for reconstruction: %w", err)
		}

		// 创建输出文件用于重建
		outFiles := make([]*os.File, len(shards))
		defer func() {
			for _, f := range outFiles {
				if f != nil {
					f.Close()
				}
			}
		}()

		outputWriters := make([]io.Writer, len(shards))
		needReconstruction := false

		// 检查哪些分片需要重建
		for i := range shards {
			if shards[i] == nil {
				needReconstruction = true
				outfn := fmt.Sprintf("%s.%d", fname, i)
				log.Printf("  Creating missing shard: %s", outfn)
				f, err := os.Create(outfn)
				if err != nil {
					return fmt.Errorf("create missing shard %d: %w", i, err)
				}
				outFiles[i] = f
				outputWriters[i] = f
			}
		}

		if needReconstruction {
			log.Println("Reconstructing data...")
			err = enc.Reconstruct(shards, outputWriters)
			if err != nil {
				return fmt.Errorf("reconstruct failed: %w", err)
			}

			// 关闭并重新打开
			for i := range shards {
				if c, ok := shards[i].(io.Closer); ok && shards[i] != nil {
					c.Close()
				}
			}
			for _, f := range outFiles {
				if f != nil {
					f.Close()
				}
			}

			// 重新打开验证
			shards, sz, err = openInput(dataShards, parityShards, fname)
			if err != nil {
				return fmt.Errorf("reopen after reconstruction: %w", err)
			}

			ok, err = enc.Verify(shards)
			if !ok {
				return fmt.Errorf("verification failed after reconstruction")
			}
			if err != nil {
				return fmt.Errorf("verify after reconstruction: %w", err)
			}

			log.Println("✓ Reconstruction successful")
		}
	} else {
		log.Println("✓ All shards verified successfully")
	}

	// 创建最终输出文件
	log.Printf("Writing reconstructed data to %s\n", outFile)
	f, err := os.Create(outFile)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer f.Close()

	// 计算输出大小
	outSize := totalSize
	if fileSize > 0 && fileSize < int(totalSize) {
		outSize = int64(fileSize)
	}

	log.Printf("Joining shards with outSize=%d\n", outSize)

	// 合并分片
	err = enc.Join(f, shards, outSize)
	if err != nil {
		return fmt.Errorf("join failed: %w", err)
	}

	// 验证输出文件
	outStat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat output: %w", err)
	}

	log.Printf("✓ Successfully decoded file: %s\n", outFile)
	log.Printf("  Original file size: %d bytes\n", fileSize)
	log.Printf("  Reconstructed size: %d bytes\n", outStat.Size())

	if outStat.Size() != int64(fileSize) {
		log.Printf("⚠️ Warning: Expected %d bytes, got %d bytes", fileSize, outStat.Size())
	}

	return nil
}

func TestRsDec1(t *testing.T) {
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

	// 记录开始时的内存状态
	var memStatsStart, memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	
	conn, err := utils.CreateUDPListener(":3400")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// 确保接收目录存在
	recvDir := filepath.Dir(fnameW)
	if err := os.MkdirAll(recvDir, 0755); err != nil {
		t.Fatal(err)
	}

	totalShards := dataShards + parityShards

	// 计算每个分片的预期大小
	expectedSizes := make([]int64, totalShards)
	shardSize := (fileSize + dataShards - 1) / dataShards

	for i := 0; i < totalShards; i++ {
		if i < dataShards {
			if i == dataShards-1 { // 最后一个数据分片
				expectedSizes[i] = int64(fileSize) - int64((dataShards-1)*shardSize)
			} else {
				expectedSizes[i] = int64(shardSize)
			}
		} else { // 校验分片大小与数据分片相同
			expectedSizes[i] = int64(shardSize)
		}
		t.Logf("Shard %d expected size: %d bytes", i, expectedSizes[i])
	}

	// 创建分片文件
	inputs := make([]*os.File, totalShards)
	receivedBytes := make([]int64, totalShards)
	fileLocks := make([]sync.Mutex, totalShards) // 保护并发写入

	defer func() {
		for i, f := range inputs {
			if f != nil {
				f.Sync()
				f.Close()
				t.Logf("Shard %d final: %d/%d bytes", i, receivedBytes[i], expectedSizes[i])
			}
		}
	}()

	for i := 0; i < totalShards; i++ {
		f, err := os.Create(fmt.Sprintf("%s.%d", fnameW, i))
		if err != nil {
			t.Fatal(err)
		}
		inputs[i] = f
	}

	timeOut := 120 * time.Second
	ddl := time.Now().Add(timeOut)
	lastProgress := time.Now()
	progressInterval := 1 * time.Second

	bufPool := &sync.Pool{
		New: func() interface{} {
			return make([]byte, 8+symbolSize)
		},
	}

	// 接收循环
	for time.Now().Before(ddl) {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

		buf := bufPool.Get().([]byte)
		// ensure buffer len is full capacity before reading into it
		buf = buf[:cap(buf)]
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			// return full-capacity slice to pool for reuse
			bufPool.Put(buf[:cap(buf)])
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// 检查是否已完成
				allComplete := true
				for i := 0; i < totalShards; i++ {
					if receivedBytes[i] < expectedSizes[i] {
						allComplete = false
						break
					}
				}
				if allComplete {
					t.Log("All shards received completely")
					break
				}
				continue
			}
			t.Logf("Read error: %v", err)
			continue
		}

		if n < 8 {
			bufPool.Put(buf[:cap(buf)])
			continue
		}

		seqNum := binary.BigEndian.Uint64(buf[:8])
		shardIdx := uint32(seqNum >> 32)
		symbolIdx := uint32(seqNum & 0xFFFFFFFF)

		if int(shardIdx) >= totalShards {
			t.Logf("Invalid shard index: %d", shardIdx)
			bufPool.Put(buf[:cap(buf)])
			continue
		}

		data := buf[8:n]
		offset := int64(symbolIdx) * int64(symbolSize)

		// 检查是否超出预期范围
		if offset+int64(len(data)) > expectedSizes[shardIdx] {
			t.Logf("Warning: Symbol exceeds shard size: shard=%d, offset=%d, size=%d, max=%d",
				shardIdx, offset, len(data), expectedSizes[shardIdx])
			bufPool.Put(buf[:cap(buf)])
			continue
		}

		// 加锁写入
		fileLocks[shardIdx].Lock()
		nWritten, err := inputs[shardIdx].WriteAt(data, offset)
		fileLocks[shardIdx].Unlock()

		if err != nil {
			t.Fatalf("Write error at shard %d, offset %d: %v", shardIdx, offset, err)
		}

		// 原子更新接收字节数
		receivedBytes[shardIdx] += int64(nWritten)

		bufPool.Put(buf[:cap(buf)])

		// 定期打印进度
		if time.Since(lastProgress) > progressInterval {
			totalExpected := int64(0)
			totalReceived := int64(0)
			for i := 0; i < totalShards; i++ {
				totalExpected += expectedSizes[i]
				totalReceived += receivedBytes[i]
			}

			progress := float64(totalReceived) * 100 / float64(totalExpected)
			t.Logf("Progress: %.2f%% (%d/%d bytes)", progress, totalReceived, totalExpected)

			// 打印每个分片的进度
			for i := 0; i < totalShards; i++ {
				if expectedSizes[i] > 0 {
					shardProgress := float64(receivedBytes[i]) * 100 / float64(expectedSizes[i])
					if shardProgress < 100 {
						t.Logf("  Shard %d: %.1f%% (%d/%d)", i, shardProgress, receivedBytes[i], expectedSizes[i])
					}
				}
			}

			lastProgress = time.Now()
		}
	}

	// 刷新所有文件
	for i, f := range inputs {
		if f != nil {
			f.Sync()
		}
		t.Logf("Shard %d received: %d/%d bytes (%.1f%%)",
			i, receivedBytes[i], expectedSizes[i],
			float64(receivedBytes[i])*100/float64(expectedSizes[i]))
	}

	// 检查是否所有分片都接收完整
	allComplete := true
	for i := 0; i < totalShards; i++ {
		if receivedBytes[i] < expectedSizes[i] {
			t.Logf("Shard %d incomplete: %d/%d bytes", i, receivedBytes[i], expectedSizes[i])
			allComplete = false
		}
	}

	if allComplete {
		t.Log("All data received, starting decode...")
	} else {
		t.Log("Some shards incomplete, attempting decode anyway...")
	}

	// 尝试解码
	if err := decode(fnameW); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	// 验证输出文件
	if stat, err := os.Stat(outFile); err == nil {
		t.Logf("Output file created: %s, size: %d bytes", outFile, stat.Size())
		if stat.Size() != int64(fileSize) {
			t.Errorf("Output file size mismatch: expected %d, got %d", fileSize, stat.Size())
		}
	} else {
		t.Errorf("Cannot stat output file: %v", err)
	}

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
