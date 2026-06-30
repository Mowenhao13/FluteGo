/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package sender

import (
	"FluteGo/constant"
	"FluteGo/pkg/encoder"
	"FluteGo/pkg/meta"
	pool "FluteGo/pkg/pool"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"

	"sync"
	"time"

	"FluteGo/pkg/sock"

	"golang.org/x/time/rate"
)

// Sender 发送端核心结构
// 功能说明：
//
//	负责文件数据的读取、编码、分片和网络发送
//
// 核心特性：
//   - 支持多种前向纠错编码算法
//   - 内存映射文件读取，提高大文件处理性能
//   - 速率限制控制，避免网络拥塞
//   - 符号级交织发送，抵抗突发性丢包
//
// 设计模式：
//
//	使用工作池和连接池优化资源利用
type Sender struct {
	fdtID     uint8
	toi       uint32 // Transport Object Identifier (RFC 6726)
	tsi       uint32 // Transport Session Identifier (RFC 6726)
	config    encoder.EncoderConfig
	encoder   encoder.BaseEncoder
	inputFile *os.File

	fileSize   int64
	chunkCount uint32
	fd         int

	totalSent    int64
	totalPackets int64
	totalFiles   uint16
	startTime    time.Time
	// Per-file send timing
	sendStarted          int32
	sendStart            time.Time
	sendEnd              time.Time
	rateLimiter          *rate.Limiter
	rateLimitBytesPerSec int
	sentChunkBytes       int64
	onProgress           func(int64)
	MaxConcurrentSends   int
	fatalSendErr         int32   // 原子标记：发送遇到致命错误
	dropRatio  float64 // 随机丢包比例 (0.0-1.0)，0=不丢，0.09=丢9%
	CSVEnabled           bool
}

// SetProgressCallback 设置进度回调函数

// SetSendPercentage 设置发送百分比，用于模拟随机丢包恢复测试。
// percentage: 1-100，实际发送数据包的比例。若传入 91，则有 9% 的概率丢弃每个包。
func (s *Sender) SetSendPercentage(pct int32) {
	if pct >= 100 || pct <= 0 {
		s.dropRatio = 0
	} else {
		s.dropRatio = 1.0 - float64(pct)/100.0
	}
}
func (s *Sender) SetProgressCallback(cb func(int64)) {
	s.onProgress = cb
}

// GetTotalSent 返回已发送的总字节数（线程安全）
func (s *Sender) GetTotalSent() int64 {
	return atomic.LoadInt64(&s.totalSent)
}

// initEncoderConfig 初始化编码器配置
// 功能说明：
//
//	根据元数据包中的传输选项信息（OTI）构建编码器配置
//
// 参数：
//
//	mt - 元数据包，包含文件信息和传输参数
//
// 返回值：
//
//	encoder.EncoderConfig - 初始化完成的编码器配置
//
// 配置解析逻辑：
//  1. 确定编码算法类型
//  2. 计算分块大小和对齐
//  3. 设置RS码的参数（数据分片、校验分片）
//  4. 配置冗余比率和最大包大小
//
// 特殊处理：
//
//	对于Reed-Solomon编码，需要特殊处理符号大小
func initEncoderConfig(mt *meta.MetaPkt, redundancyRatio float64) encoder.EncoderConfig {
	// 获取编码器类型
	encoderType := mt.Oti.FECEncodingID

	// 基础文件信息
	fileSize := mt.File.TransferLen
	chunkSize := mt.Oti.MaximumChunkSize
	// log.Printf("OTI MaximumChunkSize: %d", chunkSize)
	if chunkSize == 0 {
		chunkSize = uint32(constant.DefaultChunkSize)
	}
	// 符号大小处理
	symbolSize := mt.Oti.SymbolSize

	// 前向纠错参数
	dataShards := mt.Oti.DataShards      // Reed-Solomon
	parityShards := mt.Oti.ParityShards  // Reed-Solomon
	maxPacketSize := mt.MaxPacketSize

	// 构建编码器配置
	encoderConfig := encoder.EncoderConfig{
		Type:            encoder.EncoderType(encoderType),
		FileSize:        fileSize,
		ChunkSize:       chunkSize,
		SymbolSize:      symbolSize,
		DataShards:      uint16(dataShards),
		ParityShards:    uint16(parityShards),
		RedundancyRatio: redundancyRatio,
		MaxPacketSize:   maxPacketSize,
	}
	return encoderConfig
}

// InitSender 从元数据包初始化发送端
// 功能说明：
//
//	将元数据包转换为发送端配置，创建发送端实例
//
// 参数：
//
//	mt - 元数据包，包含完整的文件传输描述
//
// 返回值：
//
//	*Sender - 初始化的发送端实例
//	error - 初始化过程中的错误
//
// 路径解析：
//
//	尝试两种路径格式：直接文件路径和目录+文件名组合
func InitSender(mt *meta.MetaPkt, limiter *rate.Limiter, maxConcurrentSends int, redundancyRatio float64) (*Sender, error) {
	inputFilePath := mt.File.SendPath
	if _, err := os.Stat(inputFilePath); os.IsNotExist(err) {
		inputFilePath = filepath.Join(mt.File.SendPath, mt.File.Name)
	}

	config := initEncoderConfig(mt, redundancyRatio)
	// TOI 默认使用 FdtID,TSI 使用 0
	toi := uint32(mt.File.FdtID)
	tsi := uint32(0)
	return NewSender(inputFilePath, config, mt.File.FdtID, toi, tsi, mt.TotalFiles, limiter, maxConcurrentSends)
}

// NewSender 创建新的发送端实例
// 功能说明：
//
//	初始化发送端的所有组件，包括文件句柄、编码器、速率限制器等
//
// 参数：
//
//	inputFilePath - 输入文件的完整路径
//	config - 编码器配置参数
//	fdtID - 文件数据传输标识符
//	toi - Transport Object Identifier (RFC 6726)
//	tsi - Transport Session Identifier (RFC 6726)
//	totalFiles - 总文件数（用于计算速率限制）
//	limiter - 全局速率限制器（可选）
//	maxConcurrentSends - 最大并发发送数
//
// 返回值：
//
//	*Sender - 创建成功的发送端实例
//	error - 创建过程中的错误
//
// 关键步骤：
//  1. 打开并验证输入文件
//  2. 调整分块大小为内存页对齐
//  3. 创建指定类型的编码器
//  4. 配置速率限制器
//
// 错误处理：
//
//	文件不存在、文件为空、分块大小无效等情况
func NewSender(inputFilePath string, config encoder.EncoderConfig, fdtID uint8, toi uint32, tsi uint32, totalFiles uint16, limiter *rate.Limiter, maxConcurrentSends int) (*Sender, error) {
	// 打开输入文件
	file, err := os.Open(inputFilePath)
	if err != nil {
		return nil, err
	}

	// 获取文件信息
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}

	// 检查文件是否为空
	if info.Size() == 0 {
		file.Close()
		return nil, fmt.Errorf("input file is empty")
	}

	// 验证和调整分块大小
	chunkSize := int(config.ChunkSize)
	if chunkSize <= 0 {
		file.Close()
		return nil, fmt.Errorf("invalid chunk size: %d", config.ChunkSize)
	}

	config.ChunkSize = uint32(chunkSize)
	config.FileSize = uint64(info.Size())
	config.Fd = int(file.Fd())
	config.FName = inputFilePath

	// log.Printf("Sender initialized with ChunkSize: %d, FileSize: %d, ChunkCount: %d", config.ChunkSize, config.FileSize, (info.Size()+int64(chunkSize)-1)/int64(chunkSize))

	// 创建编码器实例
	enc, err := encoder.NewEncoder(config)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to create encoder: unsupported type %d", config.Type)
	}

	// 计算总分块数
	chunkCount := uint32((info.Size() + int64(chunkSize) - 1) / int64(chunkSize))

	// 配置速率限制器
	var rateBytesPerSec int
	if limiter != nil {
		// 如果提供了全局限制器，使用它
		rateBytesPerSec = int(limiter.Limit())
	} else {
		// 如果没有提供全局限制器，则创建独立的限制器
		// 根据总文件数平分默认带宽
		limitMbps := float64(constant.DefaultSendRateLimitMbps)
		if totalFiles > 0 {
			limitMbps /= float64(totalFiles)
		}
		limiter, rateBytesPerSec = CreateRateLimiter(limitMbps, int(config.MaxPacketSize))
		if limiter != nil {
			log.Printf("Created per-file rate limiter: %.2f Mbps", limitMbps)
		}
	}

	// 创建发送端实例
	sender := &Sender{
		fdtID:                fdtID,
		toi:                  toi,
		tsi:                  tsi,
		config:               config,
		encoder:              enc,
		inputFile:            file,
		fileSize:             info.Size(),
		chunkCount:           chunkCount,
		fd:                   int(file.Fd()),
		startTime:            time.Now(),
		rateLimiter:          limiter,
		rateLimitBytesPerSec: rateBytesPerSec,
		totalFiles:           totalFiles,
		MaxConcurrentSends:   maxConcurrentSends,
	}
	// Log redundancy parameters
	symPerChunk := (int64(config.ChunkSize) + int64(config.SymbolSize) - 1) / int64(config.SymbolSize)
	switch config.Type {
	case encoder.EncoderNoCode:
		log.Printf("[Sender] fdtID(%d) FEC=NoCode, Symbol=%d, Chunk=%d, Chunks=%d, Symbols/Chunk~%d, Overhead=0%%",
			fdtID, config.SymbolSize, config.ChunkSize, chunkCount, symPerChunk)
	case encoder.EncoderRaptorQ:
		overheadPct := (config.RedundancyRatio - 1.0) * 100
		totalSym := int64(float64(symPerChunk) * config.RedundancyRatio)
		log.Printf("[Sender] fdtID(%d) FEC=RaptorQ, Symbol=%d, Chunk=%d, Chunks=%d, Ratio=%.2f, Symbols/Chunk=%d→%d, Overhead=+%.2f%%",
			fdtID, config.SymbolSize, config.ChunkSize, chunkCount,
			config.RedundancyRatio, symPerChunk, totalSym, overheadPct)
	case encoder.EncoderReedSolomon:
		totalShards := config.DataShards + config.ParityShards
		overheadPct := float64(config.ParityShards) / float64(config.DataShards) * 100
		log.Printf("[Sender] fdtID(%d) FEC=ReedSolomon, Data=%d, Parity=%d, TotalShards=%d, Overhead=+%.2f%%, Symbol=%d, Chunk=%d, Chunks=%d",
			fdtID, config.DataShards, config.ParityShards, totalShards,
			overheadPct, config.SymbolSize, config.ChunkSize, chunkCount)
	}
	return sender, nil
}

// CreateRateLimiter 创建全局速率限制器
// 功能说明：
//
//	根据配置的总带宽和最大包大小创建速率限制器
//
// 参数：
//
//	limitMbps - 总带宽限制 (Mbps)
//	maxPacketSize - 最大数据包大小（字节）
//
// 返回值：
//
//	*rate.Limiter - 速率限制器实例
//	int - 每秒字节数限制
//
// 算法逻辑：
//  1. 转换为每秒字节数
//  2. 设置较大的突发大小以避免限制过严
//  3. 考虑包大小对突发大小的影响
func CreateRateLimiter(limitMbps float64, maxPacketSize int) (*rate.Limiter, int) {
	log.Printf("Configured rate limit: %.2f Mbps", limitMbps)

	// 检查是否需要速率限制
	if limitMbps <= 0 {
		return nil, 0
	}

	// 转换为每秒字节数
	bytesPerSec := int(limitMbps * 1_000_000 / 8)
	if bytesPerSec <= 0 {
		return nil, 0
	}

	// 计算突发大小（桶容量）- 使用较大的突发以避免限制过严
	// 设置为 0.5 秒的数据量，这样可以容纳短暂的突发流量
	burst := bytesPerSec / 2
	if burst <= 0 {
		burst = bytesPerSec
	}

	// 考虑包大小，确保突发至少能容纳一个完整的数据包
	packetSize := 1500
	if maxPacketSize > 0 {
		packetSize = int(maxPacketSize) + 8 // 包含序列号头部
	}
	if burst < packetSize {
		burst = packetSize
	}

	log.Printf("Rate limiter: %d bytes/sec, burst: %d bytes", bytesPerSec, burst)

	// 创建令牌桶速率限制器
	limiter := rate.NewLimiter(rate.Limit(float64(bytesPerSec)), burst)
	return limiter, bytesPerSec
}

type sendTask struct {
	chunkIdx uint32
	symbolID uint32
	buf      []byte
}

// processSendTask 异步处理发送任务
func (s *Sender) processSendTask(sck *sock.MsSocket, bufPool *sync.Pool, task sendTask, rng *rand.Rand) {
	buf := task.buf
	chunkIdx := task.chunkIdx
	symbolID := task.symbolID

	// 构建 LCT 头部 (RFC 6726)
	lctHeader := meta.NewLCTHeader(s.toi, uint8(s.config.Type), s.tsi)
	lctHeader.ChunkIndex = chunkIdx
	lctHeader.SymbolID = symbolID

	// 编码 LCT 头部
	lctBytes, err := lctHeader.Encode()
	if err != nil {
		log.Printf("encode LCT header failed: chunk %d symbol %d: %v", chunkIdx, symbolID, err)
		bufPool.Put(buf[:cap(buf)])
		atomic.StoreInt32(&s.fatalSendErr, 1)
		return
	}

	// 将 LCT 头部复制到缓冲区开头
	copy(buf[:meta.LCTHeaderLength], lctBytes)

	// 随机丢包：按 dropRatio 概率跳过发送
	if s.dropRatio > 0 && rng.Float64() < s.dropRatio {
		bufPool.Put(buf[:cap(buf)])
		return
	}

	// 发送数据
	if _, err := sck.Socket.WriteToUDP(buf, sck.Addr); err != nil {
			log.Printf("write failed: chunk %d symbol %d: %v", chunkIdx, symbolID, err)
		bufPool.Put(buf[:cap(buf)])
		atomic.StoreInt32(&s.fatalSendErr, 1) // 标记致命错误
		return
	}
	// 标记连接已使用
	sck.MarkSent()

	// 标记发送开始（第一次成功写）
	if atomic.CompareAndSwapInt32(&s.sendStarted, 0, 1) {
		s.sendStart = time.Now()
		// log.Printf("fdtID(%d): send started at %s", s.fdtID, s.sendStart.Format(time.RFC3339Nano))
	}
	// 返还缓冲区到池中
	bufPool.Put(buf[:cap(buf)])

	// 更新统计信息
	atomic.AddInt64(&s.totalPackets, 1)
	sent := atomic.AddInt64(&s.totalSent, int64(len(buf)))
	if s.onProgress != nil {
		s.onProgress(sent)
	}
}

// GetTotalBytesToSend 计算预计发送的总字节数（包含头部）
func (s *Sender) GetTotalBytesToSend() int64 {
	if s.config.Type == encoder.EncoderReedSolomon {
		totalSymbols := int64(s.chunkCount) * int64(s.config.DataShards+s.config.ParityShards)
		return totalSymbols * int64(s.config.SymbolSize+meta.LCTHeaderLength)
	}

	// For NoCode and RaptorQ
	symSize := int64(s.config.SymbolSize)
	if symSize == 0 {
		symSize = 1
	}

	chunkSize := int64(s.config.ChunkSize)
	fileSize := int64(s.fileSize)

	// Calculate full chunks
	fullChunks := fileSize / chunkSize
	lastChunkSize := fileSize % chunkSize

	var totalBytes int64

	// Helper to calculate bytes for a chunk of given size
	calcChunkBytes := func(size int64) int64 {
		symbols := (size + symSize - 1) / symSize
		if s.config.Type == encoder.EncoderRaptorQ {
			symbols = int64(float64(symbols) * s.config.RedundancyRatio)
			// RaptorQ typically sends full symbols
			return symbols * int64(symSize+meta.LCTHeaderLength)
		}
		// NoCode: payload is exactly size, plus headers
		return size + (symbols * meta.LCTHeaderLength)
	}

	if fullChunks > 0 {
		totalBytes += fullChunks * calcChunkBytes(chunkSize)
	}
	if lastChunkSize > 0 {
		totalBytes += calcChunkBytes(lastChunkSize)
	}

	return totalBytes
}

// Start 启动数据传输
// 功能说明：
//
//	启动文件数据的读取、编码和网络发送过程
//
// 参数：
//
//	ctx - 上下文，用于传递取消信号和控制流程
//
// 返回值：
//
//	error - 发送过程中的错误
//
// 核心流程：
//  1. 从连接池获取UDP连接
//  2. 初始化缓冲区池
//  3. 设置内存映射数据提供器
//  4. 创建发送回调函数
//  5. 调用编码器进行编码和发送
//
// 关键技术：
//   - 内存映射：提高大文件读取性能
//   - 符号级交织：抵抗突发丢包
//   - 连接池：复用网络连接
//   - 缓冲区池：减少内存分配开销
func (s *Sender) Start(ctx context.Context) error {
	var memStatsStart runtime.MemStats
	runtime.ReadMemStats(&memStatsStart)

	// 获取全局连接池
	p := pool.GetConnPool()
	if p == nil {
		return fmt.Errorf("connection pool not initialized")
	}

	// 获取文件传输连接
	var getErr error
	_, conns, getErr := p.GetFileConn(s.fdtID)
	if getErr != nil {
		return fmt.Errorf("failed to get connections for fdtID %d: %w", s.fdtID, getErr)
	}

	if len(conns) == 0 {
		return fmt.Errorf("no connections available for fdtID %d", s.fdtID)
	}

	// 使用第一个连接（简化实现，实际可轮询）
	conn := conns[0]

	// 启动保活协程，防止在准备阶段或发送间隙连接被回收
	// 特别是对于第二个文件，连接可能已经处于空闲监控状态

	// 随机丢包模式：记录 drop 比例用于日志
	if s.dropRatio > 0 {
		log.Printf("fdtID(%d): random drop mode enabled, dropRatio=%.2f (send ≈ %.0f%%)", s.fdtID, s.dropRatio, (1.0-s.dropRatio)*100)
	}

	keepAliveStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-keepAliveStop:
				return
			case <-ticker.C:
				conn.MarkSent()
			}
		}
	}()
	defer close(keepAliveStop)

	// 初始化缓冲区池
	bufPool := &sync.Pool{
		New: func() interface{} {
			// LCT header (24 bytes) + symbol size
			return make([]byte, 0, meta.LCTHeaderLength+int(s.config.SymbolSize))
		},
	}

	workerCount := runtime.NumCPU()
	// 创建发送任务通道
	taskChan := make(chan sendTask, 2048)
	var wgSender sync.WaitGroup
	wgSender.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		// 每个 worker 独立随机源，避免全局 rand 锁竞争导致分布不均
		rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(i)))
		// 启动发送工作协程
		go func(rng *rand.Rand) {
			defer wgSender.Done()
			for task := range taskChan {
				s.processSendTask(conn, bufPool, task, rng)
			}
		}(rng)
	}

	// 移除 mmap，改用直接文件读取
	// mappedData, mapErr := mmap.Map(s.inputFile, mmap.RDONLY, 0)
	// if mapErr != nil {
	// 	return fmt.Errorf("mmap file failed: %w", mapErr)
	// }
	// defer mappedData.Unmap()

	// 数据提供器函数 - 通过 ReadAt 直接读取文件
	// 这种方式对超大文件更稳定，且不会占用大量虚拟内存
	provider := func(chunkIdx uint32) ([]byte, int, error) {
		if chunkIdx >= s.chunkCount {
			return nil, 0, fmt.Errorf("chunk index out of bounds")
		}

		// 计算文件偏移
		offset := int64(chunkIdx) * int64(s.config.ChunkSize)
		if offset >= s.fileSize {
			return nil, 0, fmt.Errorf("chunk offset out of bounds")
		}

		// 计算分块长度
		length := int64(s.config.ChunkSize)
		if offset+length > s.fileSize {
			length = s.fileSize - offset
		}

		// 分配缓冲区并读取数据
		// 注意：这里每次都会分配内存，但对于大文件传输，相比 mmap 的缺页中断开销，
		// 这种方式更可控，且 Go 的 GC 对这种短期小对象处理很快。
		// 如果追求极致性能，可以使用 sync.Pool 复用这个 buffer，
		// 但考虑到 encoder 可能会持有这个 slice，需要小心处理生命周期。
		data := make([]byte, length)
		n, err := s.inputFile.ReadAt(data, offset)
		if err != nil && err != io.EOF {
			return nil, 0, fmt.Errorf("read file failed at offset %d: %w", offset, err)
		}

		// ReadAt 可能返回少于请求的数据（如果遇到 EOF），但我们已经计算了 length
		// 所以如果 n < length 且没有 error，通常是不应该发生的（除非文件被截断）
		if int64(n) < length {
			// 调整 slice 大小
			data = data[:n]
		}

		return data, n, nil
	}

	// 创建发送回调函数
	callback := encoder.NewChunkSendCallback(ctx, func(chunkIdx uint32, symbolID uint32, chunkSz uint32, symbolData []byte) error {
		// 应用速率限制
		if s.rateLimiter != nil {
			packetBytes := len(symbolData) + meta.LCTHeaderLength // LCT header (24 bytes)
			if err := s.rateLimiter.WaitN(ctx, packetBytes); err != nil {
				return fmt.Errorf("rate limit wait failed: %w", err)
			}
		}

		// 从池中获取缓冲区并复制数据
		buf := bufPool.Get().([]byte)
		needed := meta.LCTHeaderLength + len(symbolData)
		if cap(buf) < needed {
			buf = make([]byte, needed)
		}
		buf = buf[:needed]
		copy(buf[meta.LCTHeaderLength:], symbolData)

		// 发生致命错误时停止编码，防止空转
		if atomic.LoadInt32(&s.fatalSendErr) == 1 {
			bufPool.Put(buf[:cap(buf)])
			return fmt.Errorf("fatal send error")
		}
		// 发送到通道
		select {
		case taskChan <- sendTask{chunkIdx: chunkIdx, symbolID: symbolID, buf: buf}:
		case <-ctx.Done():
			bufPool.Put(buf[:cap(buf)])
			return ctx.Err()
		}
		return nil
	})

	// 启动编码和发送过程
	var err error
	err = s.encoder.Encode(ctx, s.chunkCount, provider, callback)

	// 关闭通道并等待发送完成
	close(taskChan)
	wgSender.Wait()

	// 标记发送结束并记录时间（如果曾经开始过）
	if atomic.LoadInt32(&s.sendStarted) == 1 {
		s.sendEnd = time.Now()
		dur := s.sendEnd.Sub(s.sendStart)
		totalBytes := atomic.LoadInt64(&s.totalSent)

		seconds := dur.Seconds()
		if seconds <= 0 {
			seconds = 0.000001 // 防止除以零，对于极小文件假设1微秒
		}

		mbps := (float64(totalBytes) * 8.0 / seconds) / 1e6

		log.Printf("fdtID(%d): send finished at %s, duration=%s", s.fdtID, s.sendEnd.Format(time.RFC3339Nano), dur.String())
		log.Printf("fdtID(%d): bytes sent=%d, duration=%s, throughput=%.4f Mbps", s.fdtID, totalBytes, dur.String(), mbps)
		log.Printf("fdtID(%d): total chunks sent=%d", s.fdtID, s.chunkCount)

		// 计算有效传输速率 (Goodput)
		goodput := (float64(s.fileSize) * 8.0 / seconds) / 1e6

		log.Printf("fdtID(%d): file size=%d, effective rate (goodput)=%.4f Mbps", s.fdtID, s.fileSize, goodput)
		// Redundancy statistics
		totalPackets := atomic.LoadInt64(&s.totalPackets)
		headerBytes := totalPackets * 8
		symbolPayload := totalBytes - headerBytes // pure symbol data, without 8-byte seq header
		wireOverhead := totalBytes - s.fileSize
		symOverhead := symbolPayload - s.fileSize
		wireRatio := float64(totalBytes) / float64(s.fileSize)
		symRatio := float64(symbolPayload) / float64(s.fileSize)
		log.Printf("fdtID(%d): redundancy stats: packets=%d, file=%d bytes, header=%d bytes",
			s.fdtID, totalPackets, s.fileSize, headerBytes)
		log.Printf("fdtID(%d):   symbol layer: payload=%d bytes, ratio=%.4f, overhead=+%d bytes (+%.2f%%)",
			s.fdtID, symbolPayload, symRatio, symOverhead, (symRatio-1.0)*100)
		log.Printf("fdtID(%d):   wire layer:   total=%d bytes, ratio=%.4f, overhead=+%d bytes (+%.2f%%)",
			s.fdtID, totalBytes, wireRatio, wireOverhead, (wireRatio-1.0)*100)

		// FEC-specific details — also compute baseSymbols for CSV logging
		var baseSymbols int64
		switch s.config.Type {
		case encoder.EncoderRaptorQ:
			symSz := int64(s.config.SymbolSize)
			if symSz == 0 {
				symSz = 1
			}
			chunkSz := int64(s.config.ChunkSize)
			lastSz := s.fileSize % chunkSz
			fullCh := s.fileSize / chunkSz
			baseSymbols = fullCh * ((chunkSz + symSz - 1) / symSz)
			if lastSz > 0 {
				baseSymbols += (lastSz + symSz - 1) / symSz
			}
			totalSymWithRatio := int64(float64(baseSymbols) * s.config.RedundancyRatio)
			log.Printf("fdtID(%d):   RaptorQ detail: baseSymbols~%d, withRatio=%d, actualSent=%d, extra=%+d",
				s.fdtID, baseSymbols, totalSymWithRatio, totalPackets, totalPackets-int64(baseSymbols))
		case encoder.EncoderReedSolomon:
			totalShards := int64(s.config.DataShards + s.config.ParityShards)
			dataSymbols := int64(s.chunkCount) * int64(s.config.DataShards)
			baseSymbols = dataSymbols
			expectedSymbols := int64(s.chunkCount) * totalShards
			log.Printf("fdtID(%d):   ReedSolomon detail: dataSymbols=%d, totalShards=%d, expectedSymbols=%d, actualSent=%d",
				s.fdtID, dataSymbols, totalShards, expectedSymbols, totalPackets)
		case encoder.EncoderNoCode:
			symSz := int64(s.config.SymbolSize)
			if symSz == 0 {
				symSz = 1
			}
			baseSymbols = (s.fileSize + symSz - 1) / symSz
			log.Printf("fdtID(%d):   NoCode detail: baseSymbols=%d, actualSent=%d, overhead=0",
				s.fdtID, baseSymbols, totalPackets)
		}

		// Write CSV performance record (if enabled)
		if s.CSVEnabled {
			csvDir := filepath.Dir(s.config.FName)
			csvPath := filepath.Join(csvDir, "sender_performance.csv")
			writeSendCSV(
				csvPath, (1.0-s.dropRatio)*100,
				s.fdtID, fecTypeName(s.config.Type), filepath.Base(s.config.FName),
				s.fileSize, s.config.ChunkSize, s.config.SymbolSize, s.chunkCount,
				s.config.RedundancyRatio,
				s.sendEnd, dur, mbps, goodput,
				totalPackets, totalBytes, headerBytes, symbolPayload,
				symRatio, wireRatio, symOverhead, wireOverhead,
				baseSymbols,
			)
		}

		// Memory Profile
		var memStatsEnd runtime.MemStats
		runtime.ReadMemStats(&memStatsEnd)
		// fmt.Printf("=== Memory Profile Results for Current Send Session ===\n")
		// fmt.Printf("Total Allocated Memory: %v bytes\n", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
		// fmt.Printf("Peak Heap Memory: %v bytes, %v MB\n", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
		// fmt.Printf("System Memory (Sys): %d MB\n", memStatsEnd.Sys/(1024*1024))
		// fmt.Printf("Heap Idle Memory: %d MB\n", memStatsEnd.HeapIdle/(1024*1024))
		// fmt.Printf("Garbage Collection Count: %v\n", memStatsEnd.NumGC-memStatsStart.NumGC)
		// fmt.Printf("Memory Allocation Count: %v\n", memStatsEnd.Mallocs-memStatsStart.Mallocs)
		// fmt.Printf("Heap Objects Count: %v\n", memStatsEnd.HeapObjects)
	}
	return err
}

// fecTypeName returns human-readable name for the FEC encoder type.
func fecTypeName(t encoder.EncoderType) string {
	switch t {
	case encoder.EncoderNoCode:
		return "NoCode"
	case encoder.EncoderRaptorQ:
		return "RaptorQ"
	case encoder.EncoderReedSolomon:
		return "ReedSolomon"
	default:
		return "Unknown"
	}
}

// writeSendCSV appends a single sender performance record to the CSV file,
// creating the file with a header row if it does not exist yet.
func writeSendCSV(csvPath string, sendPct float64,
	fdtID uint8, fecType string, fileName string, fileSize int64,
	chunkSize uint32, symbolSize uint16, chunks uint32, configRatio float64,
	sendEnd time.Time, dur time.Duration, throughputMbps float64, goodputMbps float64,
	totalPackets int64, totalBytes int64, headerBytes int64, symbolPayload int64,
	symRatio float64, wireRatio float64, symOverhead int64, wireOverhead int64,
	baseSymbols int64) {

	needHeader := false
	if _, err := os.Stat(csvPath); os.IsNotExist(err) {
		needHeader = true
	}

	f, err := os.OpenFile(csvPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[CSV] failed to open/create %s: %v", csvPath, err)
		return
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if needHeader {
		w.Write([]string{
			"Timestamp", "FdtID", "FEC", "FileName", "FileSize",
			"ChunkSize", "SymbolSize", "Chunks", "ConfigRatio", "SendPercentage",
			"DurationSec", "ThroughputMbps", "GoodputMbps",
			"TotalPackets", "TotalBytes", "HeaderBytes", "SymbolPayload",
			"SymRatio", "WireRatio", "SymOverheadBytes", "WireOverheadBytes",
			"BaseSymbols~",
		})
	}

	record := []string{
		sendEnd.Format(time.RFC3339),
		strconv.Itoa(int(fdtID)),
		fecType,
		fileName,
		strconv.FormatInt(fileSize, 10),
		strconv.FormatUint(uint64(chunkSize), 10),
		strconv.Itoa(int(symbolSize)),
		strconv.FormatUint(uint64(chunks), 10),
		strconv.FormatFloat(configRatio, 'f', 2, 64),
			strconv.FormatFloat(sendPct, 'f', 1, 64),
		strconv.FormatFloat(dur.Seconds(), 'f', 6, 64),
		strconv.FormatFloat(throughputMbps, 'f', 4, 64),
		strconv.FormatFloat(goodputMbps, 'f', 4, 64),
		strconv.FormatInt(totalPackets, 10),
		strconv.FormatInt(totalBytes, 10),
		strconv.FormatInt(headerBytes, 10),
		strconv.FormatInt(symbolPayload, 10),
		strconv.FormatFloat(symRatio, 'f', 4, 64),
		strconv.FormatFloat(wireRatio, 'f', 4, 64),
		strconv.FormatInt(symOverhead, 10),
		strconv.FormatInt(wireOverhead, 10),
		strconv.FormatInt(baseSymbols, 10),
	}

	if err := w.Write(record); err != nil {
		log.Printf("[CSV] write error: %v", err)
	}
}
