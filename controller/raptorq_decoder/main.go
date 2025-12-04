package main

import (
	"encoding/binary"
	"log"
	"math"
	"net"
	"os"
	"sync"
	"sync/atomic"

	raptorq "github.com/xssnick/raptorq"
)

const (
	savePath        = "/home/halllo-pi-v1/Projects/Flute_test_v2/cmd/received_files/recovered_1024mb.bin"
	symbolSize      = 1024
	defaultChunkSz  = 1 * 1024 * 1024
	serverAddr      = ":3400"
	packetHeaderLen = 18
)

type chunkState struct {
	sync.Mutex
	decoder     *raptorq.Decoder
	baseSymbols uint32
	chunkBytes  uint32
	received    map[uint32]struct{}
	decoded     bool
	decoding    bool // 标记是否正在解码
}

type decoderManager struct {
	mu       sync.RWMutex
	chunks   map[uint32]*chunkState
	file     *os.File
	fileMu   sync.Mutex
	fileSize int64
}

var totalWritten uint64

func main() {
	file, err := os.Create(savePath)
	if err != nil {
		log.Fatalf("创建文件失败: %v", err)
	}
	defer file.Close()

	mgr := &decoderManager{
		chunks: make(map[uint32]*chunkState),
		file:   file,
	}

	addr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		log.Fatalf("解析地址失败: %v", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatalf("监听失败: %v", err)
	}
	defer conn.Close()

	log.Printf("开始监听 %s，保存路径: %s", serverAddr, savePath)
	log.Printf("符号大小: %d bytes, 默认块大小: %d bytes", symbolSize, defaultChunkSz)

	buf := make([]byte, 128*1024*1024)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("读取错误: %v", err)
			continue
		}
		if n < packetHeaderLen {
			log.Printf("数据包过短: %d bytes，需要至少 %d 字节", n, packetHeaderLen)
			continue
		}

		baseSymbols, chunkBytes, blockID, symbolID, payload, ok := parsePacket(buf[:n])
		if !ok {
			continue
		}

		log.Printf("收到 %s -> 源块[%d] 符号[%d] 基础符号:%d 数据:%d bytes", clientAddr, blockID, symbolID, baseSymbols, len(payload))
		mgr.processSymbol(baseSymbols, chunkBytes, blockID, symbolID, payload)
	}
}

func parsePacket(data []byte) (baseSymbols, chunkBytes, blockID, symbolID uint32, payload []byte, ok bool) {
	if len(data) < packetHeaderLen {
		return 0, 0, 0, 0, nil, false
	}

	baseSymbols = binary.BigEndian.Uint32(data[0:4])
	chunkBytes = binary.BigEndian.Uint32(data[4:8])
	blockID = binary.BigEndian.Uint32(data[8:12])
	symbolID = binary.BigEndian.Uint32(data[12:16])
	dataLen := binary.BigEndian.Uint16(data[16:18])

	if len(data) < int(packetHeaderLen+int(dataLen)) {
		log.Printf("数据不完整: 期待 %d bytes, 实际 %d", packetHeaderLen+int(dataLen), len(data))
		return 0, 0, 0, 0, nil, false
	}

	payload = make([]byte, dataLen)
	copy(payload, data[packetHeaderLen:packetHeaderLen+int(dataLen)])
	return baseSymbols, chunkBytes, blockID, symbolID, payload, true
}

func (dm *decoderManager) processSymbol(baseSymbols, chunkBytes, blockID, symbolID uint32, data []byte) {
	chunk := dm.ensureChunk(blockID, baseSymbols, chunkBytes)
	if chunk == nil {
		return
	}

	chunk.Lock()
	if chunk.decoded || chunk.decoding {
		chunk.Unlock()
		return
	}
	if _, exists := chunk.received[symbolID]; exists {
		chunk.Unlock()
		return
	}

	canDecode, err := chunk.decoder.AddSymbol(symbolID, data)
	if err != nil {
		chunk.Unlock()
		log.Printf("源块 %d 添加符号失败: %v", blockID, err)
		return
	}
	chunk.received[symbolID] = struct{}{}

	needed := requiredSymbols(chunk.baseSymbols, chunk.chunkBytes)
	have := len(chunk.received)

	if have < needed && !canDecode {
		chunk.Unlock()
		return
	}

	// 开始异步解码
	chunk.decoding = true
	chunk.Unlock()

	go func() {
		success, decoded, err := chunk.decoder.Decode()
		if err != nil {
			log.Printf("源块 %d 解码失败: %v", blockID, err)
			chunk.Lock()
			chunk.decoding = false
			chunk.Unlock()
			return
		}
		if !success {
			chunk.Lock()
			chunk.decoding = false
			chunk.Unlock()
			return
		}

		writeLen := int(chunk.chunkBytes)
		if writeLen > len(decoded) {
			writeLen = len(decoded)
		}

		offset := int64(blockID) * int64(defaultChunkSz)
		dm.fileMu.Lock()
		if _, err := dm.file.WriteAt(decoded[:writeLen], offset); err != nil {
			dm.fileMu.Unlock()
			log.Printf("写入源块 %d 失败: %v", blockID, err)
			return
		}
		if end := offset + int64(writeLen); end > dm.fileSize {
			dm.fileSize = end
		}
		dm.fileMu.Unlock()

		chunk.Lock()
		chunk.decoded = true
		chunk.decoding = false
		chunk.Unlock()

		dm.mu.Lock()
		delete(dm.chunks, blockID)
		dm.mu.Unlock()

		atomic.AddUint64(&totalWritten, uint64(writeLen))
		log.Printf("✓ 源块 %d 解码成功, 写入 %d bytes (累计 %.2f MB)", blockID, writeLen, float64(atomic.LoadUint64(&totalWritten))/(1024*1024))
	}()
}

func (dm *decoderManager) ensureChunk(blockID uint32, baseSymbols, chunkBytes uint32) *chunkState {
	if chunkBytes == 0 {
		chunkBytes = defaultChunkSz
	}
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if chunk, ok := dm.chunks[blockID]; ok {
		if chunk.baseSymbols == 0 {
			chunk.baseSymbols = baseSymbols
		}
		if chunk.chunkBytes == 0 {
			chunk.chunkBytes = chunkBytes
		}
		return chunk
	}

	rq := raptorq.NewRaptorQ(uint32(symbolSize))
	dec, err := rq.CreateDecoder(chunkBytes)
	if err != nil {
		log.Printf("创建源块 %d 解码器失败: %v", blockID, err)
		return nil
	}

	chunk := &chunkState{
		decoder:     dec,
		baseSymbols: baseSymbols,
		chunkBytes:  chunkBytes,
		received:    make(map[uint32]struct{}),
	}
	dm.chunks[blockID] = chunk
	return chunk
}

func requiredSymbols(baseSymbols, chunkBytes uint32) int {
	if baseSymbols > 0 {
		return int(baseSymbols)
	}
	symbols := math.Ceil(float64(chunkBytes) / float64(symbolSize))
	if symbols < 1 {
		symbols = 1
	}
	return int(symbols)
}
