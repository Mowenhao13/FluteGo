package test

import (
	"FluteGo/pkg/utils"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"

	rs "github.com/klauspost/reedsolomon"
	"golang.org/x/sys/unix"
	"golang.org/x/time/rate"
)

var (
	dataShards   = 4
	parityShards = 2
	outDir       = "./tmp/"
	fname        = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1024mb.bin"
)

func encode(fname string) error {
	enc, err := rs.NewStream(dataShards, parityShards)
	if err != nil {
		return err
	}

	log.Println("Opening file for encoding:", fname)
	f, err := os.Open(fname)
	if err != nil {
		return err
	}
	defer f.Close()

	instat, err := f.Stat()
	if err != nil {
		return err
	}

	fileSize := instat.Size()
	totalShards := dataShards + parityShards
	log.Printf("File size: %d bytes (%.2f MB)\n", fileSize, float64(fileSize)/1024/1024)

	out := make([]*os.File, totalShards)
	dir, file := filepath.Split(fname)
	if outDir != "" {
		dir = outDir
	}

	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	for i := range out {
		outfn := fmt.Sprintf("%s.%d", file, i)
		fullPath := filepath.Join(dir, outfn)
		out[i], err = os.Create(fullPath)
		if err != nil {
			return err
		}
	}

	data := make([]io.Writer, dataShards)
	for i := range data {
		data[i] = out[i]
	}

	log.Println("Starting encoding...")
	err = enc.Split(f, data, fileSize)
	if err != nil {
		return err
	}

	log.Println("Encoding parity...")
	input := make([]io.Reader, dataShards)
	for i := range data {
		if err := out[i].Close(); err != nil {
			return err
		}
		f, err := os.Open(out[i].Name())
		if err != nil {
			return err
		}
		defer f.Close()
		input[i] = f
	}

	parity := make([]io.Writer, parityShards)
	for i := range parity {
		parity[i] = out[dataShards+i]
		defer out[dataShards+i].Close()
	}

	err = enc.Encode(input, parity)
	if err != nil {
		return err
	}

	fmt.Printf("File successfully split into %d data + %d parity shards.\n", dataShards, parityShards)
	fmt.Printf("To decode use: go run stream-decoder.go -size %d %s\n", fileSize, fname)
	return nil
}

func TestRsEnc1(t *testing.T) {

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


	if err := encode(fname); err != nil {
		t.Fatal(err)
	}

	fs, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := utils.CreateUDPConnection("192.168.1.103:3400")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	bufPool := &sync.Pool{
		New: func() interface{} {
			// 8字节序号 + symbol大小
			return make([]byte, 8+symbolSize)
		},
	}

	mmapRegions := make([][]byte, 0, len(fs))
	defer func() {
		// 清理所有 mmap 区域
		for _, region := range mmapRegions {
			if region != nil {
				unix.Munmap(region)
			}
		}
	}()

	limiter := rate.NewLimiter(rate.Limit(50*1024*1024), 512*1024) // 10 MB/s 限速
	
	// 发送循环
	// totalSymbols := 0
	for shardIdx, fn := range fs {
		if !fn.Type().IsRegular() {
			continue
		}
		// 检查文件名是否符合分片格式
		if !strings.HasPrefix(fn.Name(), "test_1024mb.bin.") {
			continue
		}

		t.Logf("Found shard: %s\n", fn.Name())

		f, err := os.Open(filepath.Join(outDir, fn.Name()))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		inStat, err := f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		log.Printf("Shard %s size: %d bytes\n", fn.Name(), inStat.Size())

		sz := int(inStat.Size())

		// 每个文件是独立的，都应该从0开始映射
		shardData, err := unix.Mmap(int(f.Fd()), 0, sz, unix.PROT_READ, unix.MAP_SHARED)
		if err != nil {
			t.Fatalf("mmap failed: %v", err)
		}

		// 保存映射以便后续清理
		mmapRegions = append(mmapRegions, shardData)

		// 验证映射的正确性
		if len(shardData) != sz {
			t.Fatalf("mmap size mismatch: expected %d, got %d", sz, len(shardData))
		}
		ctx := context.Background()

		for i := 0; i < sz; i += symbolSize {
			buf := bufPool.Get().([]byte)
			start := i
			end := i + symbolSize
			if end > sz {
				end = sz
			}

			symbolIdx := i / symbolSize
			symData := shardData[start:end]

			needed := 8 + len(symData)
			if cap(buf) < needed {
				buf = make([]byte, needed)
			}
			buf = buf[:needed]

			seqNum := (uint64(shardIdx) << 32) | uint64(symbolIdx)
			binary.BigEndian.PutUint64(buf[:8], seqNum)
			copy(buf[8:], symData)
			if err := limiter.WaitN(ctx, len(buf)); err != nil {
				t.Fatalf("Rate limiter error: %v", err)
			}
			if _, err := conn.Write(buf); err != nil {
				bufPool.Put(buf[:cap(buf)])
				t.Fatalf("Send failed: %v", err)
			}
			bufPool.Put(buf[:cap(buf)])
		}
	}

	t.Log("All shards sent successfully")

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
