package main

// import (
// 	"encoding/binary"
// 	"fmt"
// 	"log"
// 	"math"
// 	"net"
// 	"os"
// 	"time"

// 	raptorq "github.com/xssnick/raptorq"
// 	"golang.org/x/sys/unix"
// )

// const (
// 	filePath        = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1024mb.bin"
// 	symbolSize      = 1024
// 	chunkSize       = 1 * 1024 * 1024 // 1MB
// 	redundancyRatio = 1.4
// 	serverAddr      = "192.168.1.102:3400"
// 	windowSize      = 10   // 同时处理的源块数量
// 	batchPerBlock   = 4    // 每次交错发送的符号个数
// 	targetRateMBps  = 100.0 // 目标发送速率 (MB/s)
// )

// const packetHeaderSize = 18

// type activeBlock struct {
// 	id           uint32
// 	data         []byte
// 	encoder      *raptorq.Encoder
// 	baseSymbols  uint32
// 	totalSymbols uint32
// 	nextSymbol   uint32
// 	chunkSize    int
// }

// func main() {
// 	file, err := os.Open(filePath)
// 	if err != nil {
// 		log.Fatalf("打开文件失败: %v", err)
// 	}
// 	defer file.Close()

// 	stat, err := file.Stat()
// 	if err != nil {
// 		log.Fatalf("获取文件信息失败: %v", err)
// 	}

// 	fileSize := stat.Size()
// 	chunkCount := int((fileSize + int64(chunkSize) - 1) / int64(chunkSize))
// 	log.Printf("文件大小: %d bytes, 分块数: %d, 窗口大小: %d, 目标速率: %.2f MB/s", fileSize, chunkCount, windowSize, targetRateMBps)

// 	remoteAddr, err := net.ResolveUDPAddr("udp", serverAddr)
// 	if err != nil {
// 		log.Fatalf("解析目标地址失败: %v", err)
// 	}

// 	conn, err := net.DialUDP("udp", nil, remoteAddr)
// 	if err != nil {
// 		log.Fatalf("建立UDP连接失败: %v", err)
// 	}
// 	defer conn.Close()

// 	var (
// 		sendingWindow []*activeBlock
// 		nextBlock     = 0
// 		totalSent     uint64
// 		startTime     = time.Now()
// 		bytesSent     int64
// 	)

// 	flushWindow := func(index int) {
// 		blk := sendingWindow[index]
// 		if blk == nil {
// 			return
// 		}
// 		unix.Munmap(blk.data)
// 		sendingWindow = append(sendingWindow[:index], sendingWindow[index+1:]...)
// 	}

// 	for nextBlock < chunkCount || len(sendingWindow) > 0 {
// 		for len(sendingWindow) < windowSize && nextBlock < chunkCount {
// 			blk, err := loadSourceBlock(file, fileSize, nextBlock)
// 			if err != nil {
// 				log.Printf("加载源块 %d 失败: %v", nextBlock, err)
// 				nextBlock++
// 				continue
// 			}
// 			sendingWindow = append(sendingWindow, blk)
// 			nextBlock++
// 		}

// 		if len(sendingWindow) == 0 {
// 			break
// 		}

// 		for i := 0; i < len(sendingWindow); {
// 			blk := sendingWindow[i]
// 			if blk.nextSymbol >= blk.totalSymbols {
// 				log.Printf("源块 %d 发送完成 (%d 符号)", blk.id, blk.totalSymbols)
// 				flushWindow(i)
// 				continue
// 			}

// 			sent := sendSymbolBatch(conn, blk, batchPerBlock)
// 			totalSent += uint64(sent)
// 			bytesSent += int64(sent) * (int64(symbolSize) + packetHeaderSize)

// 			if blk.nextSymbol >= blk.totalSymbols {
// 				log.Printf("源块 %d 发送完成 (%d 符号)", blk.id, blk.totalSymbols)
// 				flushWindow(i)
// 				continue
// 			}
// 			i++
// 		}

// 		if len(sendingWindow) > 0 {
// 			expectedDuration := time.Duration(float64(bytesSent) / (targetRateMBps * 1024 * 1024) * float64(time.Second))
// 			if elapsed := time.Since(startTime); elapsed < expectedDuration {
// 				time.Sleep(expectedDuration - elapsed)
// 			}
// 		}
// 	}

// 	log.Printf("文件发送完成，总发送符号数: %d", totalSent)
// }

// func loadSourceBlock(file *os.File, fileSize int64, blockIndex int) (*activeBlock, error) {
// 	start := blockIndex * chunkSize
// 	end := start + chunkSize
// 	if end > int(fileSize) {
// 		end = int(fileSize)
// 	}
// 	blockSize := end - start
// 	if blockSize <= 0 {
// 		return nil, fmt.Errorf("无效的块大小: %d", blockSize)
// 	}

// 	data, err := unix.Mmap(int(file.Fd()), int64(start), blockSize, unix.PROT_READ, unix.MAP_SHARED)
// 	if err != nil {
// 		return nil, fmt.Errorf("mmap 源块失败: %w", err)
// 	}

// 	rq := raptorq.NewRaptorQ(uint32(symbolSize))
// 	enc, err := rq.CreateEncoder(data)
// 	if err != nil {
// 		unix.Munmap(data)
// 		return nil, fmt.Errorf("创建编码器失败: %w", err)
// 	}

// 	baseSymbols := enc.BaseSymbolsNum()
// 	if baseSymbols == 0 {
// 		baseSymbols = 1
// 	}

// 	totalSymbols := uint32(math.Ceil(float64(baseSymbols) * redundancyRatio))
// 	if totalSymbols < baseSymbols {
// 		totalSymbols = baseSymbols
// 	}

// 	return &activeBlock{
// 		id:           uint32(blockIndex),
// 		data:         data,
// 		encoder:      enc,
// 		baseSymbols:  baseSymbols,
// 		totalSymbols: totalSymbols,
// 		nextSymbol:   0,
// 		chunkSize:    blockSize,
// 	}, nil
// }

// func sendSymbolBatch(conn *net.UDPConn, blk *activeBlock, batchSize int) int {
// 	sent := 0
// 	for i := 0; i < batchSize && blk.nextSymbol < blk.totalSymbols; i++ {
// 		symbol := blk.encoder.GenSymbol(blk.nextSymbol)
// 		if symbol == nil {
// 			log.Printf("警告: 源块 %d 符号 %d 生成失败", blk.id, blk.nextSymbol)
// 			blk.nextSymbol++
// 			continue
// 		}

// 		packet := createPacket(blk.baseSymbols, uint32(blk.chunkSize), blk.id, blk.nextSymbol, symbol)
// 		if _, err := conn.Write(packet); err != nil {
// 			log.Printf("发送符号失败: %v", err)
// 			return sent
// 		}

// 		blk.nextSymbol++
// 		sent++

// 		if blk.nextSymbol%100 == 0 {
// 			log.Printf("源块 %d 进度: %d/%d", blk.id, blk.nextSymbol, blk.totalSymbols)
// 		}
// 	}
// 	return sent
// }

// func createPacket(baseSymbols, chunkBytes, blockID, symbolID uint32, data []byte) []byte {
// 	packet := make([]byte, packetHeaderSize+len(data))
// 	binary.BigEndian.PutUint32(packet[0:4], baseSymbols)
// 	binary.BigEndian.PutUint32(packet[4:8], chunkBytes)
// 	binary.BigEndian.PutUint32(packet[8:12], blockID)
// 	binary.BigEndian.PutUint32(packet[12:16], symbolID)
// 	binary.BigEndian.PutUint16(packet[16:18], uint16(len(data)))
// 	copy(packet[packetHeaderSize:], data)
// 	return packet
// }
