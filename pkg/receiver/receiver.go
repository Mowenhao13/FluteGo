/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package receiver

import (
	constant "FluteGo/constant"
	"FluteGo/pkg/decoder"
	"FluteGo/pkg/io"
	"FluteGo/pkg/meta"
	"FluteGo/pkg/pool"
	"FluteGo/pkg/sock"
	"FluteGo/pkg/utils"
	"context"
	stdErrors "errors"
	"math"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"sync"
	"sync/atomic"
	"time"
)

// Report 定义进度报告结构
// 功能说明：
//
//	用于向上层报告文件接收的实时进度和状态
type Report struct {
	FdtID    uint8 // 文件数据传输标识符
	Received int64 // 已接收字节数
	Total    int64 // 文件总字节数
	Status   uint8 // 传输状态：0-进行中，1-完成，2-错误
}

type reportKey struct{} // 上下文键类型，用于安全存储报告通道

// WithReportChan 将报告通道添加到上下文中
// 功能说明：
//
//	在上下文中设置报告通道，使得解码和接收循环可以将进度报告发送给监控组件
//
// 参数：
//
//	ctx - 原始上下文
//	ch  - 报告通道
//
// 返回值：
//
//	context.Context - 包含报告通道的新上下文
//
// 设计模式：
//
//	使用上下文传递组件间通信通道，避免全局变量
func WithReportChan(ctx context.Context, ch chan<- Report) context.Context {
	return context.WithValue(ctx, reportKey{}, ch)
}

// GetReportChan 从上下文中获取报告通道
// 功能说明：
//
//	从上下文中提取之前设置的报告通道
//
// 参数：
//
//	ctx - 包含报告通道的上下文
//
// 返回值：
//
//	chan<- Report - 报告通道
//	bool - 是否成功获取到通道
func GetReportChan(ctx context.Context) (chan<- Report, bool) {
	ch, ok := ctx.Value(reportKey{}).(chan<- Report)
	return ch, ok
}

// Receiver 接收端核心结构
// 功能说明：
//
//	负责网络数据的接收、解码、文件写入和进度报告
//
// 核心特性：
//   - 支持多种前向纠错解码算法
//   - 异步写入架构，提高并发性能
//   - 实时进度监控和报告
//   - 连接池复用和缓冲区复用
//
// 设计模式：
//
//	生产者-消费者模式：网络接收为生产者，文件写入为消费者
//	工作池模式：多个工作协程并行处理网络数据
type Receiver struct {
	fdtID            uint8
	toi              uint32 // Transport Object Identifier (RFC 6726)
	config           decoder.DecoderConfig
	decoder          decoder.BaseDecoder
	outputFile       *os.File
	fileMutex        sync.Mutex
	outputPath       string
		saveDir          string // 保存目录，用于写入 CSV 统计文件
	expectedMd5      string
	expectedChunks   uint32
	expectedPackets  int64    // 预期总数据包数（根据FEC参数计算）
	finishedChunks   uint32
	completedChunks  sync.Map // 用于跟踪已完成的chunk，避免重复计数
	finishChan       chan struct{}
	OnComplete       func()
	closeOnce        sync.Once
	dataChan         chan *WriteRequest
	writerWg         sync.WaitGroup
	writeRequestPool sync.Pool
	enableMd5        bool // 是否启用MD5校验
	reportChan       chan<- Report // 保存报告通道用于发送进度更新
	// 统计
	currWritten   int64     // 当前已写入字节数
	totalReceived int64     // 总共接收字节数（网络层，含 LCT 头部）
	totalPackets  int64     // 总共接收数据包数
	totalDropped  int64     // 总共丢包数
	receiveErrs   int64     // 接收错误数
		timedOut      int32     // 是否因超时而结束（原子操作）
	lastDataTime  int64     // 最后接收数据时间戳
	startTime     time.Time // 接收开始时间（如果在 newReceiver 初始化，则为创建时间）
	// per-file receive timing
	receiveStarted int32
	receiveStart   time.Time
	receiveEnd     time.Time
	lastPacketEnd  int64     // 最后一个数据包接收完成的时间戳（nanoseconds），用于纯接收速率计算
	memStatsStart  runtime.MemStats

	// 单端口架构：异步数据包队列，避免阻塞 MetaReceiver 主循环
	packetChan chan []byte
	packetWg   sync.WaitGroup
}

// WriteRequest 写入请求结构
// 功能说明：
//
//	定义从解码器到写入循环的数据传输单元
type WriteRequest struct {
	FdtID    uint8
	Data     []byte
	Offset   int64
	ChunkIdx uint32
}

// initDecoderConfig 初始化解码器配置
// 功能说明：
//
//	根据元数据包和保存目录信息构建解码器配置
//
// 参数：
//
//	mt      - 元数据包，包含文件传输参数
//	saveDir - 文件保存目录路径
//
// 返回值：
//
//	decoder.DecoderConfig - 初始化完成的解码器配置
//
// 配置解析：
//  1. 提取编码类型和文件大小
//  2. 计算分块大小，设置默认值
//  3. 配置RS解码参数（数据分片、校验分片）
//  4. 设置冗余比例和最大包大小
func initDecoderConfig(mt *meta.MetaPkt, saveDir string) decoder.DecoderConfig {
	// 从元数据提取基本参数
	decoderType := mt.Oti.FECEncodingID
	fileSize := mt.File.TransferLen
	chunkSize := mt.Oti.MaximumChunkSize
	if chunkSize == 0 {
		chunkSize = uint32(constant.DefaultChunkSize)
	}
	symbolSize := mt.Oti.SymbolSize
	dataShards := mt.Oti.DataShards
	parityShards := mt.Oti.ParityShards
	maxPacketSize := mt.MaxPacketSize

	decoderConfig := decoder.DecoderConfig{
		Type:            decoder.DecoderType(decoderType),
		FileSize:        fileSize,
		ChunkSize:       chunkSize,
		SymbolSize:      symbolSize,
		DataShards:      uint16(dataShards),
		ParityShards:    uint16(parityShards),
		MaxPacketSize:   maxPacketSize,
		FName:           constant.RsTmpRecvInDir + mt.File.Name,
		OutputPath:      saveDir + mt.File.Name, // RS解码器的最终输出路径
	}

	return decoderConfig
}

// InitReceiver 从元数据包初始化接收端
// 功能说明：
//
//	将元数据包转换为接收端配置，创建接收端实例
//
// 参数：
//
//	mt      - 元数据包，包含文件传输描述
//	saveDir - 文件保存目录
//
// 返回值：
//
//	*Receiver - 初始化的接收端实例
//	error     - 初始化过程中的错误
//
// 关键步骤：
//  1. 构建输出文件路径
//  2. 计算总分块数
//  3. 创建接收端实例
//
// 错误处理：
//
//	文件创建失败、配置无效等情况
func InitReceiver(mt *meta.MetaPkt, saveDir string, enableMd5 bool) (*Receiver, error) {
	// 构建输出文件路径
	outFilePath := saveDir + mt.File.Name
	config := initDecoderConfig(mt, saveDir)
	// ChunkSize 现在是 symbol 数量，需要转换为字节数
	chunkSize := int64(config.ChunkSize) * int64(config.SymbolSize)
	chunkCount := uint32((mt.File.TransferLen + uint64(chunkSize) - 1) / uint64(chunkSize))
	if chunkCount == 0 {
		chunkCount = 1
	}
	expectedMd5 := mt.File.Md5
	expectedPackets := calcExpectedPackets(config, chunkCount)

	// TOI 默认使用 FdtID（实际使用时应从 FDT 获取）
	toi := uint32(mt.File.FdtID)

	return newReceiver(outFilePath, config, mt.File.FdtID, toi, chunkCount, expectedPackets, expectedMd5, enableMd5)
}

// calcExpectedPackets 计算文件传输预期的总数据包数
//
// 计算规则与发送端 `rq_encoder.go` 的 `Encode` 逻辑保持一致：
//   - NoCode:   每个符号 = 1 个数据包
//   - RaptorQ:  基符号数 × RedundancyRatio（与发送端一样用 Ceil 向上取整）
//   - ReedSolomon: chunkCount × (DataShards + ParityShards)
//
// 注意：接收端只能使用配置中的 RecvRedundancyRatio 做估算，
// 实际总包数以发送端日志为准。
func calcExpectedPackets(config decoder.DecoderConfig, chunkCount uint32) int64 {
	switch config.Type {
	case decoder.DecoderReedSolomon:
		return int64(chunkCount) * int64(config.DataShards+config.ParityShards)

	case decoder.DecoderNoCode, decoder.DecoderRaptorQ:
		// ChunkSize 现在是 symbol 数量，需要转换为字节数
		chunkBytes := int64(config.ChunkSize) * int64(config.SymbolSize)
		symSize := int64(config.SymbolSize)
		if symSize <= 0 {
			symSize = 1
		}
		fileSize := int64(config.FileSize)
		if fileSize <= 0 {
			return int64(chunkCount)
		}

		// 计算总基符号数（与发送端 RqEncoder 一致）
		totalBaseSymbols := int64(0)
		for i := int64(0); i < int64(chunkCount); i++ {
			var thisChunkBytes int64
			if i < int64(chunkCount)-1 {
				thisChunkBytes = chunkBytes
			} else {
				// 最后一个chunk：剩余字节数
				thisChunkBytes = fileSize - i*chunkBytes
				if thisChunkBytes <= 0 {
					thisChunkBytes = chunkBytes
				}
			}
			baseSymbols := (thisChunkBytes + symSize - 1) / symSize
			if baseSymbols <= 0 {
				baseSymbols = 1
			}
			// 加冗余后向上取整（ceil），与发送端 RqEncoder.Encode 一致
			totalSymbols := int64(math.Ceil(float64(baseSymbols) * config.RedundancyRatio))
			if totalSymbols < int64(baseSymbols) {
				totalSymbols = int64(baseSymbols)
			}
			totalBaseSymbols += totalSymbols
		}
		if totalBaseSymbols <= 0 {
			return int64(chunkCount)
		}
		return totalBaseSymbols

	default:
		return int64(chunkCount)
	}
}

// newReceiver 创建新的接收端实例
// 功能说明：
//
//	初始化接收端的所有组件，包括文件句柄、解码器、写入循环等
//
// 参数：
//
//	outFilePath   - 输出文件完整路径
//	config        - 解码器配置
//	fdtID         - 文件数据传输标识符
//	expectedChunks   - 预期总分块数
//	expectedPackets  - 预期总数据包数（根据FEC参数计算）
//	expectedMd5      - 期望的MD5校验值
//
// 返回值：
//
//	*Receiver - 创建成功的接收端实例
//	error     - 创建过程中的错误
//
// 核心流程：
//  1. 创建并预分配输出文件
//  2. 初始化缓冲区和对象池
//  3. 创建解码器实例
//  4. 启动写入循环（针对特定解码类型）
//
// 资源管理：
//
//	使用对象池复用写入请求对象，减少GC压力
func newReceiver(outFilePath string, config decoder.DecoderConfig, fdtID uint8, toi uint32, expectedChunks uint32, expectedPackets int64, expectedMd5 string, enableMd5 bool) (*Receiver, error) {
	// 确保目录存在
	dir := filepath.Dir(outFilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %v", err)
	}

	// 创建输出文件
	file, err := os.Create(outFilePath)
	if err != nil {
		return nil, err
	}

	// 预分配文件空间(全0填充)
	file.Truncate(int64(config.FileSize))

	// 初始化接收端实例
	receiver := &Receiver{
		fdtID:          fdtID,
		toi:            toi,
		config:         config,
		outputFile:     file,
		startTime:      time.Now(),
		outputPath:     outFilePath,
			saveDir:        dir,
		expectedChunks:  expectedChunks,
		expectedPackets: expectedPackets,
		expectedMd5:     expectedMd5,
		enableMd5:      enableMd5,
		finishChan:     make(chan struct{}),
		OnComplete:     nil,
		dataChan:       make(chan *WriteRequest, 4096), // 初始化缓冲通道（增大以容纳解码突发）
		packetChan:     make(chan []byte, 16384),       // 单端口架构：异步数据包队列（增大容量减少丢包）
		writeRequestPool: sync.Pool{
			New: func() interface{} {
				return &WriteRequest{}
			},
		},
	}

	// 创建解码器实例
	dec, err := decoder.NewDecoder(config, receiver)
	if err != nil {
		file.Close()
		return nil, err
	}
	receiver.decoder = dec

	// // 启动 NcDecoder 监控（如果是 NoCode 类型）
	// if ncDec, ok := dec.(*decoder.NcDecoder); ok {
	// 	ncDec.Monitor()
	// }

	// 启动写入循环：RaptorQ/NoCode 以及 Reed-Solomon 都需要写入循环
	if receiver.config.Type == decoder.DecoderRaptorQ || receiver.config.Type == decoder.DecoderNoCode || receiver.config.Type == decoder.DecoderReedSolomon {
		receiver.startWriterLoop()
	}

	// 启动单端口架构的异步数据包消费者
	receiver.startPacketLoop()

	return receiver, nil
}

// startPacketLoop 启动异步数据包消费循环（单端口架构）
// 从 packetChan 读取数据包并调用 processPacket 处理，避免阻塞 MetaReceiver 主循环。
// 使用多个 goroutine 并行处理，提高高吞吐下的解码吞吐量。
func (r *Receiver) startPacketLoop() {
	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	if workers > 8 {
		workers = 8 // 限制最大并行度，避免过多锁竞争
	}
	for i := 0; i < workers; i++ {
		r.packetWg.Add(1)
		go func() {
			defer r.packetWg.Done()
			for data := range r.packetChan {
				r.processPacket(context.Background(), nil, data)
			}
		}()
	}
}

// EnqueuePacket 将数据包放入异步队列（单端口架构）
// 由 MetaReceiver 的 dispatchFilePacket 调用。
// 使用带超时的阻塞式入队，避免高吞吐下大量丢包导致 chunk 无法解码。
func (r *Receiver) EnqueuePacket(ctx context.Context, data []byte) error {
	select {
	case r.packetChan <- data:
		return nil
	case <-time.After(500 * time.Millisecond):
		// 超时丢弃，防止完全阻塞 MetaReceiver 主循环
		dropped := atomic.AddInt64(&r.totalDropped, 1)
		if dropped <= 5 || dropped%1000 == 0 {
			log.Printf("[Receiver] fdtID=%d: packet queue full after 500ms, dropped %d packets", r.fdtID, dropped)
		}
		return fmt.Errorf("packet queue full for fdtID=%d", r.fdtID)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startWriterLoop 启动文件写入循环
// 功能说明：
//
//	在独立的协程中运行写入循环，处理解码完成的数据写入文件
//
// 设计原理：
//  1. 从数据通道读取写入请求
//  2. 使用WriteAt进行随机写入（支持UDP乱序到达）
//  3. 更新写入统计信息
//  4. 回收写入请求对象到对象池
//
// 性能优化：
//   - 使用WriteAt直接写入，避免bufio的顺序写入限制
//   - 依赖操作系统页缓存提高写入性能
//   - 对象池复用减少内存分配
func (r *Receiver) startWriterLoop() {
	// Single writer — the buffered write channel (2560) absorbs peaks.
	// Multiple concurrent WriteAt to the same file causes handle races on close.
	workers := 1
	// log.Printf("Starting %d writer workers", workers)
	for i := 0; i < workers; i++ {
		r.writerWg.Add(1)
		go func(id int) {
			// log.Printf("Writer worker %d started for fdtID=%d", id, r.fdtID)
			defer r.writerWg.Done()
			for req := range r.dataChan {
				// log.Printf("Writer worker %d processing write request: chunkIdx=%d, offset=%d, len=%d", id, req.ChunkIdx, req.Offset, len(req.Data))
				// 使用 WriteAt 进行随机写入
				_, err := r.outputFile.WriteAt(req.Data, req.Offset)
				if err != nil {
					log.Printf("写入文件失败: %v", err)
				}

				// 更新写入统计
				atomic.AddInt64(&r.currWritten, int64(len(req.Data)))

				// 回收写入请求对象
				req.Data = nil
				r.writeRequestPool.Put(req)

				// log.Printf("Writer worker %d completed write for chunkIdx=%d, total written=%d", id, req.ChunkIdx, atomic.LoadInt64(&r.currWritten))

				// 检查是否所有分片都已接收完成
				// 使用双重保险：1. chunk去重计数 2. 字节数检查
				_, loaded := r.completedChunks.LoadOrStore(req.ChunkIdx, true)
				var finished uint32
				if !loaded {
					finished = atomic.AddUint32(&r.finishedChunks, 1)
				} else {
					finished = atomic.LoadUint32(&r.finishedChunks)
				}

				// 检查完成条件：chunk数够了，或者字节数够了
				bytesWritten := atomic.LoadInt64(&r.currWritten)
				shouldFinish := finished >= r.expectedChunks || bytesWritten >= int64(r.config.FileSize)

				var reportStatus uint8 = 0
				if shouldFinish {
					reportStatus = 1
				}

				// 发送进度报告
				if r.reportChan != nil {
					report := Report{
						FdtID:    r.fdtID,
						Received: bytesWritten,
						Total:    int64(r.config.FileSize),
						Status:   reportStatus,
					}
					select {
					case r.reportChan <- report:
						// log.Printf("Progress report sent: fdtID=%d, received=%d, total=%d, status=%d", report.FdtID, report.Received, report.Total, report.Status)
					default:
						// 非阻塞发送，避免阻塞写入循环
						// log.Printf("Progress report skipped (channel full): fdtID=%d, received=%d, total=%d", report.FdtID, report.Received, report.Total)
					}
				}

				if shouldFinish {
					r.closeOnce.Do(func() {
					close(r.finishChan)
					// 记录接收结束时间
					if atomic.LoadInt32(&r.receiveStarted) == 1 {
						r.receiveEnd = time.Now()
						// 纯接收时间：从第一个包到最后一个包接收完成（不含文件写入时间）
						lastPacketNs := atomic.LoadInt64(&r.lastPacketEnd)
						var recvEnd time.Time
						if lastPacketNs > 0 {
							recvEnd = time.Unix(0, lastPacketNs)
						} else {
							recvEnd = r.receiveEnd
						}
						recvDur := recvEnd.Sub(r.receiveStart)
						// 端到端时间（含文件写入）
						e2eDur := r.receiveEnd.Sub(r.receiveStart)

						bytesWritten := atomic.LoadInt64(&r.currWritten)
						totalRecvBytes := atomic.LoadInt64(&r.totalReceived)

						recvSeconds := recvDur.Seconds()
						if recvSeconds <= 0 {
							recvSeconds = 0.000001
						}
						e2eSeconds := e2eDur.Seconds()
						if e2eSeconds <= 0 {
							e2eSeconds = 0.000001
						}

						// 纯网络接收速率（与发送端 throughput 对齐，用网络字节数 / 接收时间）
						recvMbps := (float64(totalRecvBytes) * 8.0 / recvSeconds) / 1e6
						// 端到端有效速率（文件大小 / 端到端时间）
						e2eMbps := (float64(bytesWritten) * 8.0 / e2eSeconds) / 1e6

						fmt.Printf("File transfer completed (fdtID=%d): %d/%d chunks, recv duration=%s, e2e duration=%s\n", r.fdtID, finished, r.expectedChunks, recvDur.String(), e2eDur.String())
						fmt.Printf("fdtID(%d): bytes received=%d (wire), %d (file), throughput=%.4f Mbps (recv), %.4f Mbps (e2e)\n", r.fdtID, totalRecvBytes, bytesWritten, recvMbps, e2eMbps)

							// 包统计：预期 vs 实际
							receivedPkts := atomic.LoadInt64(&r.totalPackets)
							if r.expectedPackets > 0 {
								pktRatio := float64(receivedPkts) / float64(r.expectedPackets) * 100
								fmt.Printf("fdtID(%d): packets received=%d/%d, ratio=%.2f%%\n", r.fdtID, receivedPkts, r.expectedPackets, pktRatio)
							} else {
								fmt.Printf("fdtID(%d): packets received=%d (expected unknown)\n", r.fdtID, receivedPkts)
							}

							// Memory Profile
							var memStatsEnd runtime.MemStats
							runtime.ReadMemStats(&memStatsEnd)
							fmt.Printf("=== Memory Profile Results for Current Receive Session ===\n")
							fmt.Printf("Total Allocated Memory: %v bytes\n", memStatsEnd.TotalAlloc-r.memStatsStart.TotalAlloc)
							fmt.Printf("Peak Heap Memory: %v bytes, %v MB\n", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
							fmt.Printf("System Memory (Sys): %d MB\n", memStatsEnd.Sys/(1024*1024))
							fmt.Printf("Heap Idle Memory: %d MB\n", memStatsEnd.HeapIdle/(1024*1024))
							fmt.Printf("Garbage Collection Count: %v\n", memStatsEnd.NumGC-r.memStatsStart.NumGC)
							fmt.Printf("Memory Allocation Count: %v\n", memStatsEnd.Mallocs-r.memStatsStart.Mallocs)
							fmt.Printf("Heap Objects Count: %v\n", memStatsEnd.HeapObjects)
							// 写入 CSV 统计
							stats := r.CollectStats("completed")
							go WriteTransferCSV(r.saveDir, stats)
						} else {
							fmt.Printf("File transfer completed (fdtID=%d): %d/%d chunks\n", r.fdtID, finished, r.expectedChunks)
							// 写入 CSV 统计
							stats := r.CollectStats("completed")
							go WriteTransferCSV(r.saveDir, stats)
						}
					})
				}
			}
		}(i)
	}
}

// OnDecodedData 实现OutputHandler接口 - 解码数据回调
// 功能说明：
//
//	当解码器完成一个数据块解码时调用，将数据放入写入队列
//
// 参数：
//
//	data     - 解码后的数据
//	offset   - 文件写入偏移量
//	chunkIdx - 分块索引
//
// 返回值：
//
//	error - 入队失败的错误
//
// 核心逻辑：
//  1. 从对象池获取写入请求对象
//  2. 填充数据参数
//  3. 尝试放入数据通道
//  4. 检查是否所有分块都已完成
//
// 阻塞处理：
//
//	通道满时记录警告并返回错误，避免阻塞解码器
func (r *Receiver) OnDecodedData(data []byte, offset int64, chunkIdx uint32) error {
	// 从对象池获取写入请求对象
	req := r.writeRequestPool.Get().(*WriteRequest)
	req.FdtID = r.fdtID
	req.Data = data
	req.Offset = offset
	req.ChunkIdx = chunkIdx

	// Debug: Check channel status
	if len(r.dataChan) > cap(r.dataChan)*9/10 {
		log.Printf("WARNING: Write queue nearly full: %d/%d. Receiver may block.", len(r.dataChan), cap(r.dataChan))
	}

	// 直接发送到通道，减少逻辑判断和Timer开销
	// 如果通道满，这里会阻塞，形成自然的背压，阻止接收端接收过快
	r.dataChan <- req

	// log.Printf("OnDecodedData: chunk %d, offset %d, len %d", chunkIdx, offset, len(data))
	return nil
}

// showDecoderInfo 显示解码器配置信息
// 功能说明：
//
//	打印解码器的详细配置信息，用于调试和监控
//
// 显示内容：
//  1. 解码器类型和版本
//  2. 文件大小和分块信息
//  3. 前向纠错参数
//  4. 网络传输参数
func (r *Receiver) showDecoderInfo() {
	config := r.config
	log.Printf("=== 解码器配置信息 ===")

	// 解码器类型
	var decoderType string
	switch config.Type {
	case decoder.DecoderRaptorQ:
		decoderType = "RaptorQ"
	case decoder.DecoderReedSolomon:
		decoderType = "ReedSolomon"
	case decoder.DecoderNoCode:
		decoderType = "NoCode"
	default:
		decoderType = "Unknown"
	}
	log.Printf("解码器类型: %s", decoderType)

	// 文件信息
	log.Printf("文件大小: %d bytes (%.2f MB)",
		config.FileSize,
		float64(config.FileSize)/(1024*1024))

	// 根据解码器类型显示不同的参数
	switch config.Type {
	case decoder.DecoderRaptorQ, decoder.DecoderNoCode:
		log.Printf("符号大小: %d bytes", config.SymbolSize)
		log.Printf("Chunk大小: %d symbols (%d bytes)", config.ChunkSize, int64(config.ChunkSize)*int64(config.SymbolSize))

case decoder.DecoderReedSolomon:
		log.Printf("数据分片: %d", config.DataShards)
		log.Printf("校验分片: %d", config.ParityShards)
		log.Printf("总分片: %d", config.DataShards+config.ParityShards)
		log.Printf("Chunk大小: %d bytes", config.ChunkSize)
	}

	log.Printf("最大数据包大小: %d bytes", config.MaxPacketSize)
	log.Printf("")
}

// ShowBasicInfo 显示接收端基本信息
// 功能说明：
//
//	公开接口，用于显示接收端的配置和状态信息
func (r *Receiver) ShowBasicInfo() {
	r.showDecoderInfo()
}

// Start 启动接收过程
// 功能说明：
//
//	启动网络接收、解码和文件写入的完整流程
//
// 参数：
//
//	ctx - 上下文，用于传递取消信号和控制流程
//
// 返回值：
//
//	error - 接收过程中的错误
//
// 核心流程：
//  1. 从连接池获取UDP连接
//  2. 启动多个接收工作协程
//  3. 等待完成信号或错误
//  4. 清理资源并回调完成函数
//
// 并发控制：
//   - 为每个连接启动独立的接收循环
//   - 使用等待组同步所有协程
//   - 错误传播和上下文取消链
func (r *Receiver) Start(ctx context.Context) error {
	// 获取全局连接池
	p := pool.GetConnPool()
	if p == nil {
		return fmt.Errorf("connection pool not initialized")
	}

	// 获取文件传输连接
	_, conns, err := p.GetFileConn(r.fdtID)
	if err != nil {
		return fmt.Errorf("failed to get connections for fdtID %d: %v", r.fdtID, err)
	}
	if len(conns) == 0 {
		return fmt.Errorf("no connections available for fdtID %d", r.fdtID)
	}

	return r.runLifecycle(ctx, conns)
}

// RunPassive 以被动模式运行接收器（RFC 6726 统一端口架构）
//
// 在单端口模式下，数据包由上层 dispatcher 根据 TOI 分发给对应 Receiver，
// 接收器本身不持有 socket，仅负责超时监控、解码写入和完成回调。
func (r *Receiver) RunPassive(ctx context.Context) error {
	return r.runLifecycle(ctx, nil)
}

// runLifecycle 运行接收器生命周期：超时监控、等待完成、MD5 校验、资源清理。
// 当 conns 不为空时，为每个连接启动 readLoop；为空时则进入被动模式，等待外部包。
func (r *Receiver) runLifecycle(ctx context.Context, conns []*sock.MsSocket) error {
	runtime.ReadMemStats(&r.memStatsStart)

	// 保存报告通道以便在写入协程中使用
	if ch, ok := GetReportChan(ctx); ok {
		r.reportChan = ch
	}

	// 创建会话级上下文
	sessionCtx, sessionCancel := context.WithCancel(ctx)

	errChCap := len(conns)
	if errChCap == 0 {
		errChCap = 1
	}
	errCh := make(chan error, errChCap)
	var wg sync.WaitGroup

	// 确保资源清理
	defer func() {
		sessionCancel()
		wg.Wait()
		r.Close()
		if r.OnComplete != nil {
			r.OnComplete()
		}
	}()

	// 启动超时监控：合并 FDT 过期和文件传输超时逻辑
	// FDT 过期时间从 constant.FDT_EXPIRES 获取（Unix 时间戳）
	// 文件传输超时使用 constant.IDLE_DATA_TIMEOUT
	idleTimeout := time.Duration(constant.IDLE_DATA_TIMEOUT) * time.Second
	idleCheckInterval := 3 * time.Second

	// 计算 FDT 过期时间点
	var fdtExpiresTime time.Time
	if constant.FDT_EXPIRES > 0 {
		fdtExpiresTime = time.Unix(int64(constant.FDT_EXPIRES), 0)
	}

	go func() {
		ticker := time.NewTicker(idleCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-ticker.C:
				// 检查 FDT 是否过期
				if !fdtExpiresTime.IsZero() && time.Now().After(fdtExpiresTime) {
					got := atomic.LoadInt64(&r.currWritten)
					chunks := atomic.LoadUint32(&r.finishedChunks)
					if chunks >= r.expectedChunks || got >= int64(r.config.FileSize) {
						log.Printf("fdtID(%d): FDT expired, all chunks decoded (%d/%d, %d/%d bytes)\n", r.fdtID, chunks, r.expectedChunks, got, int64(r.config.FileSize))
						r.closeOnce.Do(func() { close(r.finishChan) })
						return
					}
					log.Printf("fdtID(%d): FDT EXPIRED — received %d/%d bytes (%.1f%%), %d/%d chunks", r.fdtID, got, int64(r.config.FileSize), float64(got)*100/float64(int64(r.config.FileSize)), chunks, r.expectedChunks)
					r.MarkTimedOut()
					stats := r.CollectStats("fdt_expired")
					go WriteTransferCSV(r.saveDir, stats)
					r.closeOnce.Do(func() { close(r.finishChan) })
					return
				}

				last := atomic.LoadInt64(&r.lastDataTime)
				// 场景1：数据传输中但中途停止 → lastDataTime 不再更新 → IDLE_DATA_TIMEOUT后超时
				if last > 0 && time.Since(time.Unix(last, 0)) > idleTimeout {
					got := atomic.LoadInt64(&r.currWritten)
					chunks := atomic.LoadUint32(&r.finishedChunks)
					// 先检查文件是否已完全解码（RaptorQ 解码可能滞后于收包）
					// 如果所有 chunk 都已解码写入，报告完成而非超时
					if chunks >= r.expectedChunks || got >= int64(r.config.FileSize) {
						log.Printf("fdtID(%d): idle, all chunks decoded (%d/%d, %d/%d bytes)\n", r.fdtID, chunks, r.expectedChunks, got, int64(r.config.FileSize))
						r.closeOnce.Do(func() { close(r.finishChan) })
						return
					}
					log.Printf("fdtID(%d): DATA IDLE TIMEOUT (%ds) — written %d/%d bytes (%.1f%%), %d/%d chunks, received %d bytes in %d packets",
					r.fdtID, constant.IDLE_DATA_TIMEOUT, got, int64(r.config.FileSize), float64(got)*100/float64(int64(r.config.FileSize)), chunks, r.expectedChunks,
					atomic.LoadInt64(&r.totalReceived), atomic.LoadInt64(&r.totalPackets))
					r.MarkTimedOut()
					stats := r.CollectStats("timeout")
					go WriteTransferCSV(r.saveDir, stats)
					r.closeOnce.Do(func() { close(r.finishChan) })
					return
				}
				// 场景2：自创建以来从未收到任何数据 → lastDataTime == 0 → IDLE_DATA_TIMEOUT*2后强制超时
				if last == 0 && time.Since(r.startTime) > idleTimeout*2 {
					log.Printf("fdtID(%d): no data received since start (%ds timeout), finishing receive", r.fdtID, constant.IDLE_DATA_TIMEOUT*2)
					r.MarkTimedOut()
					// 写入 CSV 统计（超时但未收到数据）
					stats := r.CollectStats("timeout")
					go WriteTransferCSV(r.saveDir, stats)
					r.closeOnce.Do(func() { close(r.finishChan) })
					return
				}
			}
		}
	}()

	// 为每个连接启动接收协程
	for _, conn := range conns {
		wrapper := conn
		wg.Add(1)
		go func() {
			defer wg.Done()
			connCtx, connCancel := context.WithCancel(sessionCtx)
			defer connCancel()

			if err := r.readLoop(connCtx, wrapper); err != nil && err != context.Canceled {
				select {
				case errCh <- err:
				default:
				}
				sessionCancel()
			}
		}()
	}

	// 等待完成信号
	select {
	case <-sessionCtx.Done():
	case <-r.finishChan: // 文件接收完成信号
	}

	if r.enableMd5 {
		// 验证MD5
		recvMd5, err := utils.CalculateMd5(r.outputFile)
		if err != nil {
			log.Printf("Failed to calculate MD5: %v", err)
		} else if recvMd5 != r.expectedMd5 {
			log.Printf("MD5 mismatch: expected %s, got %s", r.expectedMd5, recvMd5)
		} else {
			log.Printf("MD5 verified successfully: %s", recvMd5)
		}
	}

	// 检查是否有错误发生
	select {
	case err := <-errCh:
		if err == nil || stdErrors.Is(err, context.Canceled) || stdErrors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	default:
		return nil
	}
}

// readLoop 连接接收循环
// 功能说明：
//
//	管理单个UDP连接的数据接收，使用 IOCP 模型
func (r *Receiver) readLoop(ctx context.Context, msck *sock.MsSocket) error {
	ioHandler, err := io.NewIOHandler(msck, int(r.config.MaxPacketSize))
	if err != nil {
		return fmt.Errorf("failed to create IO handler: %v", err)
	}
	ioHandler.Start()

	// 消费者负责解码，使用 NumCPU 保证吞吐
	consumerCount := runtime.NumCPU() * 2
	if consumerCount < 2 {
		consumerCount = 2
	}
	log.Printf("Starting %d consumers for connection (NumCPU=%d)", consumerCount, runtime.NumCPU())

	var wg sync.WaitGroup
	for i := 0; i < consumerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.consumeLoop(ctx, ioHandler, msck)
		}()
	}

	// 等待上下文取消
	<-ctx.Done()

	// 先停止 IO handler，唤醒阻塞在 TryDequeue 的消费者
	ioHandler.Stop()

	// 等待消费者退出
	wg.Wait()

	return ctx.Err()
}

// consumeLoop 消费者循环
// 功能说明：
//
//	从 IOCP 队列获取数据并进行解码处理
func (r *Receiver) consumeLoop(ctx context.Context, ioHandler io.IOHandler, msck *sock.MsSocket) {
	for {
		// 检查退出
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 从队列获取数据
		ctxObj, ok := ioHandler.TryDequeue()
		if !ok {
			// 避免空转占用 CPU
			time.Sleep(1 * time.Microsecond)
			continue
		}

		// 根据上下文类型提取数据
		data, ok := io.ExtractData(ctxObj)
		if !ok {
			// 未知类型，跳过
			ioHandler.ReturnContext(ctxObj)
			continue
		}

		// 处理数据
		// 注意：ioCtx.Data 是复用的，AddSymbol 必须同步处理或拷贝数据
		r.processPacket(ctx, msck, data)
		// 归还 Context，使其可用于接收新包
		ioHandler.ReturnContext(ctxObj)
	}
}

// processPacket 处理单个数据包
func (r *Receiver) processPacket(ctx context.Context, msck *sock.MsSocket, data []byte) {
	n := len(data)

	// 更新统计
	now := time.Now().Unix()
	if msck != nil {
		atomic.StoreInt64(&msck.LastUsed, now)
	}
	atomic.StoreInt64(&r.lastDataTime, now)
	atomic.StoreInt64(&r.lastPacketEnd, now) // 记录最后一个包的接收时间，用于纯接收速率计算
	atomic.AddInt64(&r.totalReceived, int64(n))
	atomic.AddInt64(&r.totalPackets, 1)
	if cp := pool.GetConnPool(); cp != nil {
		cp.AddReceived(uint64(n))
	}

	// 第一次收到数据包时打印日志，帮助诊断
	if atomic.LoadInt64(&r.totalPackets) == 1 {
		log.Printf("[Receiver] fdtID=%d: first packet received, %d bytes", r.fdtID, n)
	}
	// 每 100 个包打印一次进度，帮助诊断
	if atomic.LoadInt64(&r.totalPackets)%100 == 0 {
		log.Printf("[Receiver] fdtID=%d: processed %d packets, %d bytes received so far", r.fdtID, atomic.LoadInt64(&r.totalPackets), atomic.LoadInt64(&r.totalReceived))
	}

	if n < meta.LCTHeaderLength {
		log.Printf("[Receiver] fdtID=%d: Packet too short for LCT header: %d bytes", r.fdtID, n)
		return
	}

	// 解析 LCT 头部 (RFC 6726)
	var lctHeader meta.LCTHeader
	if err := lctHeader.Decode(data[:meta.LCTHeaderLength]); err != nil {
		log.Printf("[Receiver] fdtID=%d: Decode LCT header failed: %v", r.fdtID, err)
		return
	}

	// 根据 TOI 路由
	if lctHeader.IsFDT() {
		// TOI=0: FDT XML，由 FDTReceiver 处理
		// 这里暂时忽略，因为 FDT 接收在更上层处理
		if atomic.LoadInt64(&r.totalPackets) <= 3 {
			log.Printf("[Receiver] fdtID=%d: dropping FDT packet (TOI=0) in processPacket", r.fdtID)
		}
		return
	}

	// TOI>0: 文件数据
	// 验证 TOI 是否匹配当前接收的文件
	if lctHeader.TOI != r.toi {
		if atomic.LoadInt64(&r.totalPackets) <= 5 {
			log.Printf("[Receiver] fdtID=%d: TOI mismatch: expected %d, got %d", r.fdtID, r.toi, lctHeader.TOI)
		}
		return
	}

	chunkIdx := lctHeader.ChunkIndex
	symbolIdx := lctHeader.SymbolID

	// 标记接收开始（第一次有效数据）
	if atomic.CompareAndSwapInt32(&r.receiveStarted, 0, 1) {
		r.receiveStart = time.Now()
		// log.Printf("fdtID(%d): receive started at %s", r.fdtID, r.receiveStart.Format(time.RFC3339Nano))
	}

	// 提交给解码器
	if err := r.decoder.AddSymbol(chunkIdx, symbolIdx, data[meta.LCTHeaderLength:n]); err != nil {
		// 仅记录错误，不退出接收循环
		log.Printf("AddSymbol failed for chunk %d, symbol %d: %v", chunkIdx, symbolIdx, err)
		return
	}

}

// HandlePacket 处理外部派发过来的单个数据包（RFC 6726 统一端口架构）
//
// 在单端口模式下，接收端只维护一个 socket，由上层根据 TOI 将数据包分发给对应 Receiver。
func (r *Receiver) HandlePacket(ctx context.Context, data []byte) {
	r.processPacket(ctx, nil, data)
}

// GetBytesWritten 返回当前已解码写入的字节数（线程安全）
func (r *Receiver) GetBytesWritten() int64 {
	return atomic.LoadInt64(&r.currWritten)
}

// MarkTimedOut 标记接收器因超时而结束
func (r *Receiver) MarkTimedOut() {
	atomic.StoreInt32(&r.timedOut, 1)
}

// IsTimedOut 返回是否因超时而结束
func (r *Receiver) IsTimedOut() bool {
	return atomic.LoadInt32(&r.timedOut) == 1
}


// Close 关闭接收端
// 功能说明：
//
//	安全关闭接收端的所有组件，释放资源
//
// 关闭顺序：
//  1. 关闭数据通道
//  2. 等待写入循环完成
//  3. 关闭解码器
//  4. 同步并关闭文件
//
// 线程安全：
//
//	通过等待组确保写入循环完成后再关闭文件
func (r *Receiver) Close() {
	// 关闭数据包队列，停止异步消费循环
	close(r.packetChan)
	r.packetWg.Wait()

	// 关闭数据通道，停止写入循环
	close(r.dataChan)
	r.writerWg.Wait()

	// Recovery summary — prints for ALL completion paths (success / timeout / error)
	chunks := atomic.LoadUint32(&r.finishedChunks)
	if r.expectedChunks > 0 {
		if chunks >= r.expectedChunks {
			extra := chunks - r.expectedChunks
			log.Printf("fdtID(%d): recovery SUMMARY: chunks %d/%d written (+%d via RaptorQ), file reassembled successfully",
				r.fdtID, chunks, r.expectedChunks, extra)
		} else {
			log.Printf("fdtID(%d): recovery FAILED: chunks %d/%d written (%d missing), FILE INCOMPLETE — no symbols received for %d chunks",
				r.fdtID, chunks, r.expectedChunks, r.expectedChunks-chunks, r.expectedChunks-chunks)
		}
	}

	// 关闭解码器
	if r.decoder != nil {
		r.decoder.Close()
	}

	// 同步文件系统并关闭文件
	r.outputFile.Sync()
	r.outputFile.Close()
}

// getRecoveredCount extracts RaptorQ recovery count from the decoder.
func (r *Receiver) getRecoveredCount() uint32 {
	if rq, ok := r.decoder.(*decoder.RqDecoder); ok {
		return rq.GetRecoveredCount()
	}
	return 0
}
