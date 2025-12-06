package receiver

import (
	constant "FluteGo/constant"
	"FluteGo/pkg/decoder"
	"FluteGo/pkg/meta"
	"FluteGo/pkg/pool"
	"context"
	"encoding/binary"
	stdErrors "errors"
	"fmt"
	"log"
	"net"
	"os"

	"sync"
	"sync/atomic"
	"time"
)

// Report defines the progress report structure
type Report struct {
	FdtID    uint8
	Received int64
	Total    int64
	Status   uint8 // 0: running, 1: complete, 2: error
}

type reportKey struct{}

// WithReportChan adds the report channel to the context
func WithReportChan(ctx context.Context, ch chan<- Report) context.Context {
	return context.WithValue(ctx, reportKey{}, ch)
}

// GetReportChan retrieves the report channel from the context
func GetReportChan(ctx context.Context) (chan<- Report, bool) {
	ch, ok := ctx.Value(reportKey{}).(chan<- Report)
	return ch, ok
}

// Receiver 负责网络接收和文件写入
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
	currWritten   int64
	totalReceived int64
	totalPackets  int64
	totalDropped  int64
	receiveErrs   int64
	lastDataTime  int64
	startTime     time.Time
}

type WriteRequest struct {
	FdtID    uint8
	Data     []byte
	Offset   int64
	ChunkIdx uint32
}

func initDecoderConfig(mt *meta.MetaPkt, saveDir string) decoder.DecoderConfig {
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
		OutputPath:      saveDir + mt.File.Name, // Final output path for RS decoder
	}

	return decoderConfig
}

func InitReceiver(mt *meta.MetaPkt, saveDir string) (*Receiver, error) {
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

func newReceiver(outFilePath string, config decoder.DecoderConfig, fdtID uint8, expectedChunks uint32, expectedMd5 string) (*Receiver, error) {
	file, err := os.Create(outFilePath)
	if err != nil {
		return nil, err
	}

	file.Truncate(int64(config.FileSize))

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
		dataChan:       make(chan *WriteRequest, 10000), // 初始化缓冲通道
		writeRequestPool: sync.Pool{
			New: func() interface{} {
				return &WriteRequest{}
			},
		},
	}

	dec, err := decoder.NewDecoder(config, receiver)
	if err != nil {
		file.Close()
		return nil, err
	}
	receiver.decoder = dec

	// 启动写入循环(对于 RaptorQ 和 NoCode)
	if receiver.config.Type == decoder.DecoderRaptorQ || receiver.config.Type == decoder.DecoderNoCode {
		receiver.startWriterLoop()
	}

	return receiver, nil
}

func (r *Receiver) startWriterLoop() {
	r.writerWg.Add(1)
	go func() {
		defer r.writerWg.Done()
		for req := range r.dataChan {
			// 使用 WriteAt 进行随机写入
			// 注意：bufio.Writer 不支持 WriteAt，如果需要随机写入，必须直接使用 outputFile
			// 如果是顺序写入，可以使用 bufio.Writer
			// 这里假设是随机写入（因为 UDP 乱序），所以直接用 outputFile
			// 但为了性能，我们可以在这里做一些聚合或者直接写入

			// 由于是乱序到达，bufio.Writer 可能不适合（它只能顺序写）
			// 除非我们能保证 req 是顺序的。
			// 如果不能保证顺序，直接用 WriteAt 是最安全的。
			// 为了提高 WriteAt 性能，依赖操作系统的页缓存。

			_, err := r.outputFile.WriteAt(req.Data, req.Offset)
			if err != nil {
				log.Printf("写入文件失败: %v", err)
			}

			atomic.AddInt64(&r.currWritten, int64(len(req.Data)))
			if req.ChunkIdx%1000 == 0 {
				log.Printf("fdtID(%d): chunk %d 写入完成: %d bytes\n", req.FdtID, req.ChunkIdx, len(req.Data))
			}
			req.Data = nil
			r.writeRequestPool.Put(req)
		}
		// 循环结束，表示文件写入完成
		// 强制执行一次 GC 以释放解码过程中可能积累的大量内存
		// runtime.GC() // 移除强制 GC，避免阻塞
	}()
}

// 实现 OutputHandler 接口
func (r *Receiver) OnDecodedData(data []byte, offset int64, chunkIdx uint32) error {
	req := r.writeRequestPool.Get().(*WriteRequest)
	req.FdtID = r.fdtID
	req.Data = data
	req.Offset = offset
	req.ChunkIdx = chunkIdx

	select {
	case r.dataChan <- req:
		// 检查是否所有分片都已接收完成
		finished := atomic.AddUint32(&r.finishedChunks, 1)
		if finished >= r.expectedChunks {
			r.closeOnce.Do(func() {
				close(r.finishChan)
				log.Printf("文件接收完成 (fdtID=%d): %d/%d chunks", r.fdtID, finished, r.expectedChunks)
			})
		}
		return nil
	default:
		r.writeRequestPool.Put(req)
		// 队列满，可能会丢弃数据或者阻塞
		// 这里选择阻塞一小段时间，然后报错，或者直接阻塞（取决于对丢包的容忍度）
		// 为了防止阻塞网络接收，这里选择非阻塞丢弃并记录日志，或者使用更大的 buffer
		log.Printf("警告: 写入队列满，丢弃 Chunk %d 数据", chunkIdx)
		return fmt.Errorf("write queue full")
	}
}

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

func (r *Receiver) ShowBasicInfo() {
	r.showDecoderInfo()
}

func (r *Receiver) Start(ctx context.Context) error {
	p := pool.GetGlobalPool()
	if p == nil {
		return fmt.Errorf("connection pool not initialized")
	}

	_, conns, err := p.GetGlobalFileConn(r.fdtID)
	if err != nil {
		return fmt.Errorf("failed to get connections for fdtID %d: %v", r.fdtID, err)
	}

	if len(conns) == 0 {
		return fmt.Errorf("no connections available for fdtID %d", r.fdtID)
	}

	// session level context
	sessionCtx, sessionCancel := context.WithCancel(ctx)

	errCh := make(chan error, len(conns))
	var wg sync.WaitGroup

	defer func() {
		sessionCancel()
		wg.Wait()
		r.Close()
		if r.OnComplete != nil {
			r.OnComplete()
		}
	}()

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

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-sessionCtx.Done():
	case <-done:
	case <-r.finishChan:
	}

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

func (r *Receiver) readLoop(ctx context.Context, conn *pool.UDPConnWrapper) error {
	bufPool := &sync.Pool{
		New: func() interface{} {
			return make([]byte, r.config.MaxPacketSize*10)
		},
	}

	workerCount := 1
	if workerCount <= 0 {
		workerCount = 1
	}

	log.Printf("Starting %d read workers for connection\n", workerCount)

	// worker level context
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	errCh := make(chan error, workerCount)
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.readWorker(workerCtx, conn, bufPool, errCh)
		}()
	}

	var err error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-errCh:
	}

	workerCancel()
	// Force unblock ReadFromUDP
	conn.Conn.SetReadDeadline(time.Now())
	wg.Wait()

	// Restore deadline
	var zero time.Time
	conn.Conn.SetReadDeadline(zero)

	return err
}

func (r *Receiver) readWorker(ctx context.Context, conn *pool.UDPConnWrapper, bufPool *sync.Pool, errCh chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		buf := bufPool.Get().([]byte)
		n, _, err := conn.Conn.ReadFromUDP(buf)
		atomic.AddInt64(&r.totalReceived, int64(n))
		pool.GetGlobalPool().AddReceived(uint64(n))

		if err != nil {
			bufPool.Put(buf)
			if os.IsTimeout(err) {
				currWritten := int(atomic.LoadInt64(&r.currWritten))
				log.Printf("接收超时，已接收: %d MB, 待写入: %d MB, 丢包: %d",
					currWritten/(1024*1024),
					(r.totalReceived-int64(currWritten))/(1024*1024),
					r.totalDropped)
				continue
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

		// 优化：直接传递 buf 切片，避免 make 和 copy
		// 注意：这要求 decoder.AddSymbol 内部必须拷贝数据，或者我们必须确保 buf 在 decoder 使用完之前不被复用
		// 大多数 FEC 库（包括 raptorq）的 AddSymbol 会拷贝数据到内部结构
		if r.config.Type == decoder.DecoderRaptorQ || r.config.Type == decoder.DecoderNoCode {
			seqNum := binary.BigEndian.Uint64(buf[:8])
			// data := make([]byte, n-8)
			// copy(data, buf[8:n])
			// bufPool.Put(buf)

			chunkSize := uint64(r.config.ChunkSize)
			if chunkSize == 0 {
				bufPool.Put(buf)
				continue
			}

			chunkIdx := uint32(seqNum / chunkSize)
			symbolIdx := uint32(seqNum % chunkSize)

			if err := r.decoder.AddSymbol(chunkIdx, symbolIdx, buf[8:n]); err != nil {
				select {
				case errCh <- err:
				default:
				}
				bufPool.Put(buf)
				return
			}
		} else if r.config.Type == decoder.DecoderReedSolomon {
			seqNum := binary.BigEndian.Uint64(buf[:8])
			shardIdx := uint32(seqNum >> 32)
			symbolIdx := uint32(seqNum & 0xFFFFFFFF)

			if err := r.decoder.AddSymbol(shardIdx, symbolIdx, buf[8:n]); err != nil {
				select {
				case errCh <- err:
				default:
				}
				bufPool.Put(buf)
				return
			}
		}
		bufPool.Put(buf)

		atomic.StoreInt64(&conn.LastUsed, time.Now().Unix())

		// Report progress
		if ch, ok := GetReportChan(ctx); ok {
			select {
			case ch <- Report{
				FdtID:    r.fdtID,
				Received: atomic.LoadInt64(&r.currWritten),
				Total:    int64(r.config.FileSize),
				Status:   0,
			}:
			default:
			}
		}
	}
}

func (r *Receiver) Close() {
	close(r.dataChan)
	r.writerWg.Wait()
	if r.decoder != nil {
		r.decoder.Close()
	}
	r.outputFile.Sync()
	r.outputFile.Close()
}
