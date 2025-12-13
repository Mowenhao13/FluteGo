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
	"FluteGo/pkg/meta"
	"FluteGo/pkg/pool"
	"context"
	"encoding/binary"
	"encoding/csv"
	stdErrors "errors"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"strconv"
	"unsafe"

	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
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
	config           decoder.DecoderConfig
	decoder          decoder.BaseDecoder
	outputFile       *os.File
	fileMutex        sync.Mutex
	outputPath       string
	expectedMd5      string
	expectedChunks   uint32
	finishedChunks   uint32
	finishChan       chan struct{}
	OnComplete       func()
	closeOnce        sync.Once
	dataChan         chan *WriteRequest
	writerWg         sync.WaitGroup
	writeRequestPool sync.Pool

	// 统计
	currWritten   int64     // 当前已写入字节数
	totalReceived int64     // 总共接收字节数
	totalPackets  int64     // 总共接收数据包数
	totalDropped  int64     // 总共丢包数
	receiveErrs   int64     // 接收错误数
	lastDataTime  int64     // 最后接收数据时间戳
	startTime     time.Time // 接收开始时间（如果在 newReceiver 初始化，则为创建时间）
	// per-file receive timing
	receiveStarted int32
	receiveStart   time.Time
	receiveEnd     time.Time
	memStatsStart  runtime.MemStats
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
	redundancyRatio := constant.RecvRedundancyRatio
	maxPacketSize := mt.MaxPacketSize

	decoderConfig := decoder.DecoderConfig{
		Type:            decoder.DecoderType(decoderType),
		FileSize:        fileSize,
		ChunkSize:       chunkSize,
		SymbolSize:      symbolSize,
		DataShards:      uint16(dataShards),
		ParityShards:    uint16(parityShards),
		RedundancyRatio: redundancyRatio,
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
func InitReceiver(mt *meta.MetaPkt, saveDir string) (*Receiver, error) {
	// 构建输出文件路径
	outFilePath := saveDir + mt.File.Name
	config := initDecoderConfig(mt, saveDir)
	chunkSize := int64(config.ChunkSize)
	if chunkSize <= 0 {
		chunkSize = int64(constant.DefaultChunkSize)
	}
	chunkCount := uint32((mt.File.TransferLen + uint64(chunkSize) - 1) / uint64(chunkSize))
	if chunkCount == 0 {
		chunkCount = 1
	}
	expectedMd5 := mt.File.Md5

	return newReceiver(outFilePath, config, mt.File.FdtID, chunkCount, expectedMd5)
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
//	expectedChunks - 预期总分块数
//	expectedMd5   - 期望的MD5校验值
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
func newReceiver(outFilePath string, config decoder.DecoderConfig, fdtID uint8, expectedChunks uint32, expectedMd5 string) (*Receiver, error) {
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
		config:         config,
		outputFile:     file,
		startTime:      time.Now(),
		outputPath:     outFilePath,
		expectedChunks: expectedChunks,
		expectedMd5:    expectedMd5,
		finishChan:     make(chan struct{}),
		OnComplete:     nil,
		dataChan:       make(chan *WriteRequest, 30000), // 初始化缓冲通道（增大以容纳重组后大量回调）
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

	// 启动写入循环：RaptorQ/NoCode 以及 Reed-Solomon 都需要写入循环
	if receiver.config.Type == decoder.DecoderRaptorQ || receiver.config.Type == decoder.DecoderNoCode || receiver.config.Type == decoder.DecoderReedSolomon {
		receiver.startWriterLoop()
	}

	return receiver, nil
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
	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	log.Printf("Starting %d writer workers", workers)
	for i := 0; i < workers; i++ {
		r.writerWg.Add(1)
		go func(id int) {
			defer r.writerWg.Done()
			for req := range r.dataChan {
				// 使用 WriteAt 进行随机写入
				_, err := r.outputFile.WriteAt(req.Data, req.Offset)
				if err != nil {
					log.Printf("写入文件失败: %v", err)
				}

				// 更新写入统计
				atomic.AddInt64(&r.currWritten, int64(len(req.Data)))

				// // 每1000个分块记录一次日志
				// if req.ChunkIdx%1000 == 0 {
				// 	log.Printf("fdtID(%d) writer#%d: chunk %d 写入完成: %d bytes", req.FdtID, id, req.ChunkIdx, len(req.Data))
				// }

				// 回收写入请求对象
				req.Data = nil
				r.writeRequestPool.Put(req)
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

	// 尝试放入数据通道（若满，等待短时间后放弃以避免阻塞过久）
	queued := false
	select {
	case r.dataChan <- req:
		queued = true
	default:
		// queue full, wait briefly up to 3000ms
		timer := time.NewTimer(5000 * time.Millisecond)
		select {
		case r.dataChan <- req:
			queued = true
		case <-timer.C:
			// timeout
		}
		timer.Stop()
	}

	if !queued {
		r.writeRequestPool.Put(req)
		log.Printf("warning: write queue full and enqueue timed out for chunk %d", chunkIdx)
		return fmt.Errorf("write queue full")
	}

	// 已成功入队，检查是否所有分片都已接收完成
	finished := atomic.AddUint32(&r.finishedChunks, 1)
	if finished >= r.expectedChunks {
		r.closeOnce.Do(func() {
			close(r.finishChan)
			// 记录接收结束时间
			if atomic.LoadInt32(&r.receiveStarted) == 1 {
				r.receiveEnd = time.Now()
				dur := r.receiveEnd.Sub(r.receiveStart)
				bytesWritten := atomic.LoadInt64(&r.currWritten)

				seconds := dur.Seconds()
				if seconds <= 0 {
					seconds = 0.000001 // 防止除以零
				}
				mbps := (float64(bytesWritten) * 8.0 / seconds) / 1e6

				fmt.Printf("File transfer completed (fdtID=%d): %d/%d chunks, duration=%s\n", r.fdtID, finished, r.expectedChunks, dur.String())
				fmt.Printf("fdtID(%d): bytes received=%d, duration=%s, throughput=%.4f Mbps\n", r.fdtID, bytesWritten, dur.String(), mbps)

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

				// Write to CSV
				csvFile, err := os.OpenFile("receiver_performance.csv", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				if err == nil {
					defer csvFile.Close()
					writer := csv.NewWriter(csvFile)
					defer writer.Flush()

					// Check if file is empty to write header
					stat, _ := csvFile.Stat()
					if stat.Size() == 0 {
						writer.Write([]string{"Timestamp", "FdtID", "BytesReceived", "Duration(s)", "Throughput(Mbps)", "TotalAlloc(Bytes)", "PeakHeap(Bytes)", "SysMem(MB)", "HeapIdle(MB)", "GCCount", "Mallocs", "HeapObjects", "FEC_Type"})
					}

					fecType := "Unknown"
					switch r.config.Type {
					case decoder.DecoderNoCode:
						fecType = "NoCode"
					case decoder.DecoderRaptorQ:
						fecType = "RaptorQ"
					case decoder.DecoderReedSolomon:
						fecType = "ReedSolomon"
					}

					writer.Write([]string{
						time.Now().Format(time.RFC3339),
						strconv.Itoa(int(r.fdtID)),
						strconv.FormatInt(bytesWritten, 10),
						fmt.Sprintf("%.6f", dur.Seconds()),
						fmt.Sprintf("%.6f", mbps),
						strconv.FormatUint(memStatsEnd.TotalAlloc-r.memStatsStart.TotalAlloc, 10),
						strconv.FormatUint(memStatsEnd.HeapAlloc, 10),
						strconv.FormatUint(memStatsEnd.Sys/(1024*1024), 10),
						strconv.FormatUint(memStatsEnd.HeapIdle/(1024*1024), 10),
						strconv.FormatUint(uint64(memStatsEnd.NumGC-r.memStatsStart.NumGC), 10),
						strconv.FormatUint(memStatsEnd.Mallocs-r.memStatsStart.Mallocs, 10),
						strconv.FormatUint(memStatsEnd.HeapObjects, 10),
						fecType,
					})
				} else {
					log.Printf("Failed to open receiver_performance.csv: %v", err)
				}
			} else {
				fmt.Printf("File transfer completed (fdtID=%d): %d/%d chunks\n", r.fdtID, finished, r.expectedChunks)
			}
		})
	}

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
		log.Printf("Chunk大小: %d bytes", config.ChunkSize)
		if config.Type == decoder.DecoderRaptorQ {
			log.Printf("冗余比例: %.2f%%", config.RedundancyRatio*100)
		}

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
	runtime.ReadMemStats(&r.memStatsStart)

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

	// 创建会话级上下文
	sessionCtx, sessionCancel := context.WithCancel(ctx)

	errCh := make(chan error, len(conns))
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

	// 等待所有协程完成
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// 等待完成信号
	select {
	case <-sessionCtx.Done():
	case <-done:
	case <-r.finishChan: // 文件接收完成信号
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
//	管理单个UDP连接的数据接收，包括工作协程启动和错误处理
//
// 参数：
//
//	ctx  - 连接级上下文
//	conn - UDP连接包装器
//
// 返回值：
//
//	error - 接收过程中的错误
//
// 工作模式：
//  1. 初始化缓冲区池
//  2. 启动多个接收工作协程
//  3. 监听完成信号
//  4. 清理工作协程
func (r *Receiver) readLoop(ctx context.Context, wsck *pool.WinSocket) error {
	// 初始化缓冲区池
	bufPool := &sync.Pool{
		New: func() interface{} {
			return make([]byte, r.config.MaxPacketSize*10)
		},
	}

	// 设置工作协程数量
	workerCount := 16
	if workerCount <= 0 {
		workerCount = 1
	}

	log.Printf("Starting %d read workers for connection\n", workerCount)

	// 创建工作级上下文
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	// 启动工作协程
	errCh := make(chan error, workerCount)
	var wg sync.WaitGroup

	// 设置接收超时，以便在上下文取消时能够退出阻塞的读取
	// 设置为 2 秒
	timeout := int(2000)
	windows.SetsockoptInt(wsck.Socket, windows.SOL_SOCKET, windows.SO_RCVTIMEO, timeout)

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.readWorker(workerCtx, wsck, bufPool, errCh)
		}()
	}

	// 等待错误或取消
	var err error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-errCh:
	}

	// 清理工作协程
	workerCancel()
	// 等待所有工作协程退出，防止向已关闭的通道发送数据
	wg.Wait()
	return err
}

// readWorker 接收工作协程
// 功能说明：
//
//	从UDP连接读取数据，解析数据包，提交给解码器处理
//
// 参数：
//
//	ctx     - 工作协程上下文
//	wsck    - UDP连接
//	bufPool - 缓冲区池
//	errCh   - 错误通道
//
// 核心流程：
//  1. 从缓冲区池获取缓冲区
//  2. 读取UDP数据报
//  3. 解析数据包头部
//  4. 根据解码器类型处理数据
//  5. 发送进度报告
//
// 性能优化：
//   - 缓冲区池复用减少内存分配
//   - 原子操作更新统计信息
//   - 非阻塞进度报告
func (r *Receiver) readWorker(ctx context.Context, wsck *pool.WinSocket, bufPool *sync.Pool, errCh chan<- error) {
	// 准备WSARecvFrom参数
	var bytesRecvd uint32
	var flags uint32
	var from windows.RawSockaddrAny
	var fromLen int32 = int32(unsafe.Sizeof(from))
	var wsaBuf windows.WSABuf

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 从缓冲区池获取缓冲区
		buf := bufPool.Get().([]byte)
		wsaBuf.Len = uint32(cap(buf))
		wsaBuf.Buf = &buf[0]

		// 重置参数
		flags = 0
		fromLen = int32(unsafe.Sizeof(from))

		// 使用WSARecvFrom接收数据
		err := windows.WSARecvFrom(wsck.Socket, &wsaBuf, 1, &bytesRecvd, &flags, &from, &fromLen, nil, nil)
		n := int(bytesRecvd)

		// 更新最后活动时间，防止被连接池回收
		atomic.StoreInt64(&wsck.LastUsed, time.Now().Unix())

		// 更新接收统计
		atomic.AddInt64(&r.totalReceived, int64(n))
		pool.GetConnPool().AddReceived(uint64(n))

		if err != nil {
			bufPool.Put(buf)
			// 检查是否是超时 (WSAETIMEDOUT = 10060)
			if err == windows.WSAETIMEDOUT {
				// 接收超时是预期的（用于检查退出信号），不打印日志以免刷屏
				continue
			}
			// 检查是否是WSAEWOULDBLOCK (10035)
			if err == windows.WSAEWOULDBLOCK {
				continue
			}
			// 忽略因Socket关闭导致的错误
			if err == windows.WSAEINTR || err == windows.WSAENOTSOCK {
				return
			}

			select {
			case errCh <- err:
			default:
			}
			return
		}

		if n < 8 {
			bufPool.Put(buf)
			continue
		}

		// 根据解码器类型处理数据
		if r.config.Type == decoder.DecoderRaptorQ || r.config.Type == decoder.DecoderNoCode {
			// 解析序列号
			seqNum := binary.BigEndian.Uint64(buf[:8])

			chunkSize := uint64(r.config.ChunkSize)
			if chunkSize == 0 {
				bufPool.Put(buf)
				continue
			}

			// 计算分块索引和符号索引
			chunkIdx := uint32(seqNum / chunkSize)
			symbolIdx := uint32(seqNum % chunkSize)

			// 标记接收开始（第一次有效数据）
			if atomic.CompareAndSwapInt32(&r.receiveStarted, 0, 1) {
				r.receiveStart = time.Now()
				log.Printf("fdtID(%d): receive started at %s", r.fdtID, r.receiveStart.Format(time.RFC3339Nano))
			}

			// 提交给解码器
			if err := r.decoder.AddSymbol(chunkIdx, symbolIdx, buf[8:n]); err != nil {
				// 仅记录错误，不退出接收循环
				// log.Printf("Error processing chunk %d: %v", chunkIdx, err)
				bufPool.Put(buf)
				continue
			}
		} else if r.config.Type == decoder.DecoderReedSolomon {
			// 解析RS编码序列号
			seqNum := binary.BigEndian.Uint64(buf[:8])
			shardIdx := uint32(seqNum >> 32)
			symbolIdx := uint32(seqNum & 0xFFFFFFFF)

			// 标记接收开始（第一次有效数据）
			if atomic.CompareAndSwapInt32(&r.receiveStarted, 0, 1) {
				r.receiveStart = time.Now()
				log.Printf("fdtID(%d): receive started at %s", r.fdtID, r.receiveStart.Format(time.RFC3339Nano))
			}

			// 提交给解码器
			if err := r.decoder.AddSymbol(shardIdx, symbolIdx, buf[8:n]); err != nil {
				// 仅记录错误，不退出接收循环
				// log.Printf("Error processing shard %d: %v", shardIdx, err)
				bufPool.Put(buf)
				continue
			}
		}
		// 返还缓冲区
		bufPool.Put(buf)

		// 更新连接最后使用时间
		atomic.StoreInt64(&wsck.LastUsed, time.Now().Unix())

		// 发送进度报告
		if ch, ok := GetReportChan(ctx); ok {
			select {
			case ch <- Report{
				FdtID:    r.fdtID,
				Received: atomic.LoadInt64(&r.currWritten),
				Total:    int64(r.config.FileSize),
				Status:   0,
			}:
			default:
				// 非阻塞发送，避免阻塞接收循环
			}
		}
	}
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
	// 关闭数据通道，停止写入循环
	close(r.dataChan)
	r.writerWg.Wait()

	// 关闭解码器
	if r.decoder != nil {
		r.decoder.Close()
	}

	// 同步文件系统并关闭文件
	r.outputFile.Sync()
	r.outputFile.Close()
}
