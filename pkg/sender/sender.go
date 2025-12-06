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

	"golang.org/x/sys/unix"
	"golang.org/x/time/rate"
)

type Sender struct {
	fdtID     uint8
	config    encoder.EncoderConfig
	encoder   encoder.BaseEncoder
	inputFile *os.File

	fileSize   int64
	chunkCount uint32
	fd         int

	totalSent            int64
	totalPackets         int64
	totalFiles           uint16
	startTime            time.Time
	rateLimiter          *rate.Limiter
	rateLimitBytesPerSec int
	sentChunkBytes       int64
}

func initEncoderConfig(mt *meta.MetaPkt) encoder.EncoderConfig {
	encoderType := mt.Oti.FECEncodingID
	fileSize := mt.File.TransferLen
	chunkSize := mt.Oti.MaximumChunkSize
	if chunkSize == 0 {
		chunkSize = uint32(constant.DefaultChunkSize)
	}
	symbolSize := mt.Oti.SymbolSize
	// For Reed-Solomon we expect a packet/symbol size (e.g. MTU).
	// Some OTI constructors set SymbolSize=1 as a placeholder; prefer mt.MaxPacketSize or constant.MaxPacketSize.
	if encoderType == 2 { // Reed-Solomon
		if mt.MaxPacketSize > 0 {
			symbolSize = uint16(mt.MaxPacketSize)
		} else if symbolSize <= 1 {
			symbolSize = uint16(constant.MaxPacketSize)
		}
	} else {
		if symbolSize == 0 {
			symbolSize = uint16(constant.MaxPacketSize)
		}
	}
	dataShards := mt.Oti.DataShards
	parityShards := mt.Oti.ParityShards
	redundancyRatio := constant.SendRedundancyRatio
	maxPacketSize := mt.MaxPacketSize

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

func InitSender(mt *meta.MetaPkt) (*Sender, error) {
	inputFilePath := mt.File.SendPath
	if _, err := os.Stat(inputFilePath); os.IsNotExist(err) {
		inputFilePath = filepath.Join(mt.File.SendPath, mt.File.Name)
	}

	config := initEncoderConfig(mt)
	return NewSender(inputFilePath, config, mt.File.FdtID, mt.TotalFiles)
}

func NewSender(inputFilePath string, config encoder.EncoderConfig, fdtID uint8, totalFiles uint16) (*Sender, error) {
	file, err := os.Open(inputFilePath)
	if err != nil {
		return nil, err
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}

	if info.Size() == 0 {
		file.Close()
		return nil, fmt.Errorf("input file is empty")
	}

	chunkSize := int(config.ChunkSize)
	if chunkSize <= 0 {
		file.Close()
		return nil, fmt.Errorf("invalid chunk size: %d", config.ChunkSize)
	}

	pageSize := os.Getpagesize()
	if chunkSize%pageSize != 0 {
		chunkSize = ((chunkSize + pageSize - 1) / pageSize) * pageSize
	}
	config.ChunkSize = uint32(chunkSize)
	config.FileSize = uint64(info.Size())
	config.Fd = int(file.Fd())
	config.FName = inputFilePath

	enc, err := encoder.NewEncoder(config)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to create encoder: unsupported type %d", config.Type)
	}

	chunkCount := uint32((info.Size() + int64(chunkSize) - 1) / int64(chunkSize))
	rateLimiter, rateBytesPerSec, rateMbps := buildRateLimiter(config.MaxPacketSize, totalFiles)
	if rateLimiter != nil {
		log.Printf("Send rate limit enabled: %.2f Mbps (~%d bytes/sec)", rateMbps, rateBytesPerSec)
	}

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

func buildRateLimiter(maxPacketSize, totalFiles uint16) (*rate.Limiter, int, float64) {
	limitMbps := float64(constant.DefaultSendRateLimitMbps / totalFiles)

	log.Printf("Configured send rate limit: %.2f Mbps", limitMbps)

	if limitMbps <= 0 {
		return nil, 0, limitMbps
	}

	bytesPerSec := int(limitMbps * 1_000_000 / 8)
	if bytesPerSec <= 0 {
		return nil, 0, limitMbps
	}

	burst := bytesPerSec / 10
	if burst <= 0 {
		burst = bytesPerSec
	}

	packetSize := 1500
	if maxPacketSize > 0 {
		packetSize = int(maxPacketSize) + 8 // include sequence number header
	}
	if burst < packetSize {
		burst = packetSize
	}

	limiter := rate.NewLimiter(rate.Limit(float64(bytesPerSec)), burst)
	return limiter, bytesPerSec, limitMbps
}

func (s *Sender) writeSymbol(conn *pool.UDPConnWrapper, bufPool *sync.Pool, chunkIdx uint32, symbolID uint32, symbolData []byte) error {
	buf := bufPool.Get().([]byte)
	needed := 8 + len(symbolData)
	if cap(buf) < needed {
		buf = make([]byte, needed)
	}
	buf = buf[:needed]

	var seqNum uint64
	// If encoder configuration indicates Reed-Solomon (data+parity shards),
	// the callback for RS encoder passes shardIdx as the first argument.
	// Receiver expects seq packed as: high32=shardIdx, low32=symbolIdx.
	if s.config.DataShards > 0 && s.config.ParityShards > 0 {
		seqNum = (uint64(chunkIdx) << 32) | uint64(symbolID)
	} else {
		seqNum = uint64(chunkIdx)*uint64(s.config.ChunkSize) + uint64(symbolID)
	}
	binary.BigEndian.PutUint64(buf[:8], seqNum)
	copy(buf[8:], symbolData)

	if conn == nil || conn.Conn == nil {
		bufPool.Put(buf[:cap(buf)])
		return fmt.Errorf("no connection available to write chunk %d symbol %d", chunkIdx, symbolID)
	}

	n, err := conn.Conn.Write(buf)
	if err != nil {
		bufPool.Put(buf[:cap(buf)])
		return fmt.Errorf("write failed: chunk %d symbol %d: %w", chunkIdx, symbolID, err)
	}

	if s.config.DataShards > 0 && s.config.ParityShards > 0 {
		// RS mode: chunkIdx holds shardIdx in our packing
		log.Printf("Write symbol for shard %d symbol %d, bytes_sent=%d, payload_len=%d", chunkIdx, symbolID, n, len(symbolData))
	} else {
		log.Printf("Write symbol for chunk %d symbol %d, bytes_sent=%d, payload_len=%d", chunkIdx, symbolID, n, len(symbolData))
	}

	// mark connection used
	conn.MarkSent()

	bufPool.Put(buf[:cap(buf)])
	atomic.AddInt64(&s.totalPackets, 1)
	atomic.AddInt64(&s.totalSent, int64(len(symbolData)))

	return nil
}

// Start starts sending data in an interleaved fashion (symbol-level interleaving).
// This is useful for resisting burst losses.
func (s *Sender) Start(ctx context.Context) error {
	p := pool.GetGlobalPool()
	if p == nil {
		return fmt.Errorf("connection pool not initialized")
	}

	_, conns, err := p.GetGlobalFileConn(s.fdtID)
	if err != nil {
		return fmt.Errorf("failed to get connections for fdtID %d: %w", s.fdtID, err)
	}

	if len(conns) == 0 {
		return fmt.Errorf("no connections available for fdtID %d", s.fdtID)
	}

	// Use the first connection for simplicity, or round-robin
	conn := conns[0]

	bufPool := &sync.Pool{
		New: func() interface{} {
			// 8 bytes header + symbol size
			return make([]byte, 0, 8+int(s.config.SymbolSize))
		},
	}

	// Track mmapped data to unmap later
	mmappedData := make([][]byte, s.chunkCount)
	defer func() {
		for _, d := range mmappedData {
			if d != nil {
				unix.Munmap(d)
			}
		}
	}()

	provider := func(chunkIdx uint32) ([]byte, int, error) {
		if chunkIdx >= s.chunkCount {
			return nil, 0, fmt.Errorf("chunk index out of bounds")
		}

		// Return already mmapped data if available
		if mmappedData[chunkIdx] != nil {
			return mmappedData[chunkIdx], len(mmappedData[chunkIdx]), nil
		}

		offset := int64(chunkIdx) * int64(s.config.ChunkSize)
		if offset >= s.fileSize {
			return nil, 0, fmt.Errorf("chunk offset out of bounds")
		}

		length := int64(s.config.ChunkSize)
		if offset+length > s.fileSize {
			length = s.fileSize - offset
		}

		data, err := unix.Mmap(s.fd, offset, int(length), unix.PROT_READ, unix.MAP_SHARED)
		if err != nil {
			return nil, 0, fmt.Errorf("mmap failed: %w", err)
		}

		mmappedData[chunkIdx] = data
		return data, int(length), nil
	}

	callback := encoder.NewChunkSendCallback(ctx, func(chunkIdx uint32, symbolID uint32, chunkSz uint32, symbolData []byte) error {
		if s.rateLimiter != nil {
			packetBytes := len(symbolData) + 8 // 8 bytes header (SeqNum)
			if err := s.rateLimiter.WaitN(ctx, packetBytes); err != nil {
				return fmt.Errorf("rate limit wait failed: %w", err)
			}
		}
		return s.writeSymbol(conn, bufPool, chunkIdx, symbolID, symbolData)
	})

	return s.encoder.Encode(ctx, s.chunkCount, provider, callback)
}
