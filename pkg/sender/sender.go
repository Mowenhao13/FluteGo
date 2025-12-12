/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */
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
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"

	// "runtime"
	// "strconv"
	"sync"
	"time"

	"github.com/edsrzf/mmap-go"
	"golang.org/x/sys/windows"
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
func initEncoderConfig(mt *meta.MetaPkt) encoder.EncoderConfig {
	// 获取编码器类型
	encoderType := mt.Oti.FECEncodingID

	// 基础文件信息
	fileSize := mt.File.TransferLen
	chunkSize := mt.Oti.MaximumChunkSize
	log.Printf("OTI MaximumChunkSize: %d", chunkSize)
	if chunkSize == 0 {
		chunkSize = uint32(constant.DefaultChunkSize)
	}
	// 符号大小处理
	symbolSize := mt.Oti.SymbolSize

	// 对于Reed-Solomon编码，期望一个包/符号大小（例如MTU大小）
	// 一些OTI构造函数将SymbolSize=1作为占位符；优先使用mt.MaxPacketSize或constant.MaxPacketSize
	if encoderType == 2 { // Reed-Solomon
		if mt.MaxPacketSize > 0 {
			symbolSize = uint16(mt.MaxPacketSize)
		} else if symbolSize <= 1 {
			symbolSize = uint16(constant.MAX_PACKET_SIZE)
		}
	} else {
		if symbolSize == 0 {
			symbolSize = uint16(constant.MAX_PACKET_SIZE)
		}
	}

	// 前向纠错参数
	dataShards := mt.Oti.DataShards                 // Reed-Solomon
	parityShards := mt.Oti.ParityShards             // Reed-Solomon
	redundancyRatio := constant.SendRedundancyRatio // RaptorQ
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
func InitSender(mt *meta.MetaPkt) (*Sender, error) {
	inputFilePath := mt.File.SendPath
	if _, err := os.Stat(inputFilePath); os.IsNotExist(err) {
		inputFilePath = filepath.Join(mt.File.SendPath, mt.File.Name)
	}

	config := initEncoderConfig(mt)
	return NewSender(inputFilePath, config, mt.File.FdtID, mt.TotalFiles)
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
//	totalFiles - 总文件数（用于计算速率限制）
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
//  4. 构建速率限制器
//
// 错误处理：
//
//	文件不存在、文件为空、分块大小无效等情况
func NewSender(inputFilePath string, config encoder.EncoderConfig, fdtID uint8, totalFiles uint16) (*Sender, error) {
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

	log.Printf("Sender initialized with ChunkSize: %d, FileSize: %d, ChunkCount: %d", config.ChunkSize, config.FileSize, (info.Size()+int64(chunkSize)-1)/int64(chunkSize))

	// 创建编码器实例
	enc, err := encoder.NewEncoder(config)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to create encoder: unsupported type %d", config.Type)
	}

	// 计算总分块数
	chunkCount := uint32((info.Size() + int64(chunkSize) - 1) / int64(chunkSize))

	// 构建速率限制器
	rateLimiter, rateBytesPerSec, rateMbps := buildRateLimiter(config.MaxPacketSize, totalFiles)
	if rateLimiter != nil {
		log.Printf("Send rate limit enabled: %.2f Mbps (~%d bytes/sec)", rateMbps, rateBytesPerSec)
	}

	// 创建发送端实例
	sender := &Sender{
		fdtID:                fdtID,
		config:               config,
		encoder:              enc,
		inputFile:            file,
		fileSize:             info.Size(),
		chunkCount:           chunkCount,
		fd:                   int(file.Fd()),
		startTime:            time.Now(),
		rateLimiter:          rateLimiter,
		rateLimitBytesPerSec: rateBytesPerSec,
		totalFiles:           totalFiles,
	}
	return sender, nil
}

// buildRateLimiter 构建速率限制器
// 功能说明：
//
//	根据配置的最大包大小和总文件数计算发送速率限制
//
// 参数：
//
//	maxPacketSize - 最大数据包大小（字节）
//	totalFiles - 总并发文件数
//
// 返回值：
//
//	*rate.Limiter - 速率限制器实例
//	int - 每秒字节数限制
//	float64 - Mbps速率限制
//
// 算法逻辑：
//  1. 计算每个文件的平均速率
//  2. 转换为每秒字节数
//  3. 设置合适的突发大小
//  4. 考虑包大小对突发大小的影响
func buildRateLimiter(maxPacketSize, totalFiles uint16) (*rate.Limiter, int, float64) {
	// 计算每个文件的速率限制
	//TODO: change totalFiles to currSendingFiles
	limitMbps := float64(constant.DefaultSendRateLimitMbps / totalFiles)

	log.Printf("Configured send rate limit: %.2f Mbps", limitMbps)

	// 检查是否需要速率限制
	if limitMbps <= 0 {
		return nil, 0, limitMbps
	}

	// 转换为每秒字节数
	bytesPerSec := int(limitMbps * 1_000_000 / 8)
	if bytesPerSec <= 0 {
		return nil, 0, limitMbps
	}

	// 计算突发大小（桶容量）
	burst := bytesPerSec / 10
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

	// 创建令牌桶速率限制器
	limiter := rate.NewLimiter(rate.Limit(float64(bytesPerSec)), burst)
	return limiter, bytesPerSec, limitMbps
}

// writeSymbol 写入单个符号到网络
// 功能说明：
//
//	将编码后的数据符号封装为网络包并发送
//
// 参数：
//
//	conn - UDP连接包装器
//	bufPool - 缓冲区池，用于复用内存
//	chunkIdx - 分块索引
//	symbolID - 符号标识符
//	symbolData - 符号数据
//
// 返回值：
//
//	error - 发送过程中的错误
//
// 核心流程：
//  1. 从缓冲区池获取缓冲区
//  2. 构建数据包头部（序列号）
//  3. 复制符号数据
//  4. 通过UDP连接发送
//  5. 更新发送统计信息
//
// 特殊处理：
//
//	对于Reed-Solomon编码，序列号有特殊的编码方式
func (s *Sender) writeSymbol(wsck *pool.WinSocket, bufPool *sync.Pool, chunkIdx uint32, symbolID uint32, symbolData []byte) error {
	// 从缓冲区池获取缓冲区
	buf := bufPool.Get().([]byte)
	needed := 8 + len(symbolData)
	if cap(buf) < needed {
		buf = make([]byte, needed)
	}
	buf = buf[:needed]

	// 构建序列号
	var seqNum uint64

	// 如果编码器配置指示使用Reed-Solomon（数据+校验分片）
	// RS编码器的回调函数将分片索引作为第一个参数传递
	// 接收端期望序列号格式为：高32位=分片索引，低32位=符号索引
	if s.config.DataShards > 0 && s.config.ParityShards > 0 {
		seqNum = (uint64(chunkIdx) << 32) | uint64(symbolID)
	} else {
		seqNum = uint64(chunkIdx)*uint64(s.config.ChunkSize) + uint64(symbolID)
	}
	binary.BigEndian.PutUint64(buf[:8], seqNum)
	copy(buf[8:], symbolData)

	var wsaBuf windows.WSABuf
	var byteSent uint32

	wsaBuf.Len = uint32(len(buf))
	wsaBuf.Buf = &buf[0]

	// 发送数据
	err := windows.WSASendTo(wsck.Socket, &wsaBuf, 1, &byteSent, wsck.Flags, wsck.To.ToAny, wsck.To.ToLen, nil, nil)
	if err != nil {
		bufPool.Put(buf[:cap(buf)])
		return fmt.Errorf("write failed: chunk %d symbol %d: %w", chunkIdx, symbolID, err)
	}

	// 记录发送日志
	//TODO: Remove logging
	if chunkIdx%5000 == 0 {
		if s.config.DataShards > 0 && s.config.ParityShards > 0 {
			// RS模式：chunkIdx 保存分片索引
			log.Printf("Write symbol for shard %d symbol %d, bytes_sent=%d, payload_len=%d", chunkIdx, symbolID, byteSent, len(symbolData))
		} else {
			log.Printf("Write symbol for chunk %d symbol %d, bytes_sent=%d, payload_len=%d", chunkIdx, symbolID, byteSent, len(symbolData))
		}
	}

	// 标记连接已使用
	wsck.MarkSent()

	// 标记发送开始（第一次成功写）
	//TODO: Remove logging
	if atomic.CompareAndSwapInt32(&s.sendStarted, 0, 1) {
		s.sendStart = time.Now()
		log.Printf("fdtID(%d): send started at %s", s.fdtID, s.sendStart.Format(time.RFC3339Nano))
	}
	// 返还缓冲区到池中
	bufPool.Put(buf[:cap(buf)])

	// 更新统计信息
	atomic.AddInt64(&s.totalPackets, 1)
	atomic.AddInt64(&s.totalSent, int64(len(symbolData))+8)

	return nil
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
	//TODO: Add multi connections support
	conn := conns[0]

	// Calculate and log total symbols
	var totalSymbols int64
	if s.config.Type == encoder.EncoderReedSolomon {
		totalSymbols = int64(s.chunkCount) * int64(s.config.DataShards+s.config.ParityShards)
	} else {
		// For RaptorQ and NoCode
		symSize := int64(s.config.SymbolSize)
		if symSize == 0 {
			symSize = 1
		}
		symbolsPerChunk := (int64(s.config.ChunkSize) + symSize - 1) / symSize
		if s.config.Type == encoder.EncoderRaptorQ {
			symbolsPerChunk = int64(float64(symbolsPerChunk) * s.config.RedundancyRatio)
		}
		totalSymbols = int64(s.chunkCount) * symbolsPerChunk
	}
	log.Printf("fdtID(%d): Total symbols to send: %d", s.fdtID, totalSymbols)

	// 初始化缓冲区池
	bufPool := &sync.Pool{
		New: func() interface{} {
			// 8 bytes header + symbol size
			return make([]byte, 0, 8+int(s.config.SymbolSize))
		},
	}

	// 映射整个文件
	mappedData, mapErr := mmap.Map(s.inputFile, mmap.RDONLY, 0)
	if mapErr != nil {
		return fmt.Errorf("mmap file failed: %w", mapErr)
	}
	defer mappedData.Unmap()

	// 数据提供器函数 - 通过内存映射提供分块数据
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

		return mappedData[offset : offset+length], int(length), nil
	}

	// 创建发送回调函数
	callback := encoder.NewChunkSendCallback(ctx, func(chunkIdx uint32, symbolID uint32, chunkSz uint32, symbolData []byte) error {
		// 应用速率限制
		if s.rateLimiter != nil {
			packetBytes := len(symbolData) + 8 // 8 bytes header (SeqNum)
			if err := s.rateLimiter.WaitN(ctx, packetBytes); err != nil {
				return fmt.Errorf("rate limit wait failed: %w", err)
			}
		}
		// 发送符号
		return s.writeSymbol(conn, bufPool, chunkIdx, symbolID, symbolData)
	})

	// 启动编码和发送过程
	var err error
	err = s.encoder.Encode(ctx, s.chunkCount, provider, callback)

	// 标记发送结束并记录时间（如果曾经开始过）
	if atomic.LoadInt32(&s.sendStarted) == 1 {
		s.sendEnd = time.Now()
		dur := s.sendEnd.Sub(s.sendStart)
		totalBytes := atomic.LoadInt64(&s.totalSent)
		mbps := 0.0
		if dur.Seconds() > 0 {
			mbps = (float64(totalBytes) * 8.0 / dur.Seconds()) / 1e6
		}
		log.Printf("fdtID(%d): send finished at %s, duration=%s", s.fdtID, s.sendEnd.Format(time.RFC3339Nano), dur.String())
		log.Printf("fdtID(%d): bytes sent=%d, duration=%s, throughput=%.2f Mbps", s.fdtID, totalBytes, dur.String(), mbps)

		// 计算有效传输速率 (Goodput)
		goodput := 0.0
		if dur.Seconds() > 0 {
			goodput = (float64(s.fileSize) * 8.0 / dur.Seconds()) / 1e6
		}
		log.Printf("fdtID(%d): file size=%d, effective rate (goodput)=%.2f Mbps", s.fdtID, s.fileSize, goodput)
	}
	return err
}
