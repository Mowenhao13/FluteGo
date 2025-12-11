package test

// import (
// 	"fmt"
// 	"os"
// 	"runtime"
// 	"runtime/pprof"
// 	"sync"
// 	"sync/atomic"
// 	"testing"

// 	"github.com/xssnick/raptorq"
// 	"golang.org/x/sys/unix"
// )

// const (
// 	// workFilePath = "/home/Halllo/Projects/Flute_test_v2/cmd/work_files/test_1024mb_work.bin"
// 	symbolSize   = 1400        // RaptorQ 符号大小
// 	maxWorkers   = 2           // 并发 goroutine 数量，避免过多并发
// 	// chunkSize    = 10 * 1024 * 1024 // 10MB chunks
// )

// var globalPeakHeapAlloc uint64 // 全局峰值内存记录
// var globalPeakHeapAlloc2 uint64
// // 更新峰值内存的函数
// func updatePeakMemory() {
// 	var memStats runtime.MemStats
// 	runtime.ReadMemStats(&memStats)
// 	current := memStats.HeapAlloc

// 	// 原子操作更新峰值
// 	for {
// 		oldPeak := atomic.LoadUint64(&globalPeakHeapAlloc)
// 		if current <= oldPeak {
// 			break
// 		}
// 		if atomic.CompareAndSwapUint64(&globalPeakHeapAlloc, oldPeak, current) {
// 			break
// 		}
// 	}
// }

// func updatePeakMemory2() {
// 	var memStats runtime.MemStats
// 	runtime.ReadMemStats(&memStats)
// 	current := memStats.HeapAlloc

// 	// 原子操作更新峰值
// 	for {
// 		oldPeak := atomic.LoadUint64(&globalPeakHeapAlloc2)
// 		if current <= oldPeak {
// 			break
// 		}
// 		if atomic.CompareAndSwapUint64(&globalPeakHeapAlloc2, oldPeak, current) {
// 			break
// 		}
// 	}
// }

// // 编码单个分块，生成 K + 20% 冗余符号
// func encodeChunk(data []byte) (int, error) {
// 	// 更新峰值内存（编码前）
// 	updatePeakMemory()

// 	rq := raptorq.NewRaptorQ(symbolSize)
// 	enc, err := rq.CreateEncoder(data)
// 	if err != nil {
// 		return 0, err
// 	}

// 	// 更新峰值内存（创建encoder后）
// 	updatePeakMemory()

// 	// 获取源符号数量 K
// 	k := int(enc.BaseSymbolsNum())

// 	// 计算总符号数：K + 20% 冗余
// 	redundancy := k / 5 // 20% 冗余
// 	totalSymbols := k + redundancy

// 	// 生成所有符号（实际应用中会发送这些符号）
// 	for i := 0; i < totalSymbols; i++ {
// 		_ = enc.GenSymbol(uint32(i)) // 生成符号，但不保存（仅测试内存开销）
// 	}

// 	// 更新峰值内存（编码完成后）
// 	updatePeakMemory()

// 	return totalSymbols, nil
// }

// func recordMemStats(label string, memStatsStart *runtime.MemStats, t *testing.T) {
// 	var memStatsNow runtime.MemStats
// 	runtime.ReadMemStats(&memStatsNow)

// 	t.Logf("=== %s ===", label)
// 	t.Logf("  本阶段新增分配: %v MB", (memStatsNow.TotalAlloc-memStatsStart.TotalAlloc)/(1024*1024))
// 	t.Logf("  当前堆内存: %v MB", memStatsNow.HeapAlloc/(1024*1024))
// 	t.Logf("  GC 次数增量: %v", memStatsNow.NumGC-memStatsStart.NumGC)
// 	t.Logf("  堆对象数: %v", memStatsNow.HeapObjects)

// 	*memStatsStart = memStatsNow
// }

// // worker 函数：处理单个 chunk
// func processChunk(fd int, chunkIndex int, offset int64, length int, resultsChan chan<- struct {
// 	index int
// 	err   error
// 	symbols int
// }) {
// 	// 使用 defer 确保无论成功失败都发送结果
// 	defer func() {
// 		if r := recover(); r != nil {
// 			resultsChan <- struct {
// 				index   int
// 				err     error
// 				symbols int
// 			}{index: chunkIndex, err: fmt.Errorf("panic in chunk %d: %v", chunkIndex, r), symbols: 0}
// 		}
// 	}()

// 	// mmap 当前块（使用实际的 length）
// 	data, err := unix.Mmap(fd, offset, length, unix.PROT_READ, unix.MAP_SHARED)
// 	if err != nil {
// 		resultsChan <- struct {
// 			index   int
// 			err     error
// 			symbols int
// 		}{index: chunkIndex, err: fmt.Errorf("mmap chunk %d failed: %v", chunkIndex, err), symbols: 0}
// 		return
// 	}

// 	// 更新峰值内存
// 	updatePeakMemory()

// 	// 编码当前块
// 	numSymbols, err := encodeChunk(data)

// 	// 立即 unmmap，释放虚拟内存
// 	defer func() {
// 		if munmapErr := unix.Munmap(data); munmapErr != nil {
// 			// 只记录日志，不影响主流程
// 			fmt.Printf("Warning: deferred Munmap chunk %d failed: %v\n", chunkIndex, munmapErr)
// 		}
// 		// 更新峰值内存（munmap 后）
// 		updatePeakMemory()
// 	}()

// 	resultsChan <- struct {
// 		index   int
// 		err     error
// 		symbols int
// 	}{index: chunkIndex, err: err, symbols: numSymbols}
// }

// func TestMmapFuncMem(t *testing.T) {
// 	const chunkSize = 1024 * 1024 * 10
// 	// 创建 pprof 文件
// 	memProfile, err := os.Create("func_mmap_mem.pprof")
// 	if err != nil {
// 		t.Fatalf("Failed to create memory profile: %v", err)
// 	}
// 	defer memProfile.Close()

// 	runtime.GC()
// 	pprof.WriteHeapProfile(memProfile)

// 	var memStatsStart runtime.MemStats
// 	runtime.ReadMemStats(&memStatsStart)

// 	// 初始化峰值内存为当前值
// 	atomic.StoreUint64(&globalPeakHeapAlloc, memStatsStart.HeapAlloc)

// 	t.Logf("【阶段0】初始状态")
// 	t.Logf("  堆内存: %v MB", memStatsStart.HeapAlloc/(1024*1024))

// 	// === 阶段1：打开文件 ===
// 	file, err := os.OpenFile(workFilePath, os.O_RDONLY, 0644)
// 	if err != nil {
// 		t.Fatalf("Failed to open file: %v", err)
// 	}
// 	defer file.Close()

// 	// 获取文件大小
// 	fileInfo, err := file.Stat()
// 	if err != nil {
// 		t.Fatalf("Failed to get file info: %v", err)
// 	}
// 	fileSize := fileInfo.Size()

// 	// 计算实际的 chunk 数量（向上取整）
// 	numChunks := int((fileSize + int64(chunkSize) - 1) / int64(chunkSize))

// 	t.Logf("文件大小: %d bytes (%.2f MB)", fileSize, float64(fileSize)/(1024*1024))
// 	t.Logf("分块大小: %d bytes (%.2f MB)", chunkSize, float64(chunkSize)/(1024*1024))
// 	t.Logf("分块数量: %d chunks", numChunks)

// 	recordMemStats("【阶段1】打开大文件后", &memStatsStart, t)

// 	fd := int(file.Fd())

// 	// 使用 channel 收集结果
// 	resultsChan := make(chan struct {
// 		index   int
// 		err     error
// 		symbols int
// 	}, numChunks)

// 	// 创建 job channel
// 	jobChan := make(chan struct {
// 		index  int
// 		offset int64
// 		length int
// 	}, maxWorkers)

// 	// 创建工作池
// 	var wg sync.WaitGroup

// 	// 启动固定数量的 worker goroutine
// 	for w := 0; w < maxWorkers; w++ {
// 		wg.Add(1)
// 		go func() {
// 			defer wg.Done()
// 			for job := range jobChan {
// 				processChunk(fd, job.index, job.offset, job.length, resultsChan)
// 			}
// 		}()
// 	}

// 	// 发送所有任务到 job channel
// 	go func() {
// 		defer close(jobChan)
// 		for i := 0; i < numChunks; i++ {
// 			offset := int64(i) * int64(chunkSize)
// 			length := chunkSize
// 			// 最后一个 chunk 可能小于 chunkSize
// 			if offset+int64(length) > fileSize {
// 				length = int(fileSize - offset)
// 			}

// 			jobChan <- struct {
// 				index  int
// 				offset int64
// 				length int
// 			}{index: i, offset: offset, length: length}
// 		}
// 	}()

// 	// 关闭 resultsChan 当所有 worker 完成
// 	go func() {
// 		wg.Wait()
// 		close(resultsChan)
// 	}()

// 	// 收集结果
// 	chunkResults := make([]error, numChunks)
// 	totalSymbolsGenerated := 0
// 	completedChunks := 0

// 	for result := range resultsChan {
// 		chunkResults[result.index] = result.err
// 		if result.err == nil {
// 			totalSymbolsGenerated += result.symbols
// 		}
// 		completedChunks++

// 		// 每 10 块记录一次进度（可选）
// 		if completedChunks%10 == 0 || completedChunks == numChunks {
// 			t.Logf("✅ 已完成 %d/%d chunks", completedChunks, numChunks)
// 			runtime.GC()
// 		}
// 	}

// 	// 检查错误并输出统计信息
// 	errorCount := 0
// 	for i, err := range chunkResults {
// 		if err != nil {
// 			t.Errorf("Chunk %d failed: %v", i, err)
// 			errorCount++
// 		}
// 	}
// 	t.Logf("✅ 编码完成: %d/%d chunks 成功, 总共生成符号数: %d",
// 		numChunks-errorCount, numChunks, totalSymbolsGenerated)

// 	// === 阶段4：最终 GC 清理 ===
// 	runtime.GC()
// 	recordMemStats("【阶段4】全部处理完成 + GC 后", &memStatsStart, t)

// 	// 输出峰值内存结果
// 	finalPeak := atomic.LoadUint64(&globalPeakHeapAlloc)
// 	t.Logf("🔥 峰值内存使用: %v MB (%v bytes)", finalPeak/(1024*1024), finalPeak)
// 	t.Logf("📊 当前内存使用: %v MB", memStatsStart.HeapAlloc/(1024*1024))
// }
