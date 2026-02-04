package test

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/panjf2000/gnet/v2"
)

const (
	UDPChunkSize     = 32 * 1024 // 32KB chunks (safe for UDP)
	testFilePath     = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_100mb.bin"
	receivedFilePath = "/home/Halllo/Projects/Flute_test_v2/cmd/received_files/"
)

// UDP文件元数据
type FileMetadata struct {
	Filename    string
	Size        int64
	Checksum    string
	TotalChunks int32
}

// UDP数据块格式
type UDPFileChunk struct {
	Sequence  int32  // 序列号
	Total     int32  // 总块数
	ChunkSize int32  // 当前块大小
	Data      []byte // 数据
	IsLast    bool   // 是否最后一块
}

type gnetUDPReceiver struct {
	gnet.BuiltinEventEngine
	file         *os.File
	expectedSize int64
	receivedSize int64
	filename     string
	checksum     string
	totalChunks  int32
	receivedMap  map[int32]bool // 跟踪接收的块
	startTime    time.Time
}

type gnetUDPSender struct {
	gnet.BuiltinEventEngine
	filePath    string
	fileSize    int64
	checksum    string
	totalChunks int32
	targets     []string
}

func (s *gnetUDPSender) OnBoot(eng gnet.Engine) (action gnet.Action) {
	// 计算文件校验和
	file, err := os.Open(s.filePath)
	if err != nil {
		log.Fatalf("Failed to open file: %v", err)
		return gnet.Shutdown
	}
	defer file.Close()

	// 获取文件信息
	info, err := file.Stat()
	if err != nil {
		log.Fatalf("Failed to get file info: %v", err)
		return gnet.Shutdown
	}
	s.fileSize = info.Size()

	// 计算总块数
	s.totalChunks = int32((s.fileSize + UDPChunkSize - 1) / UDPChunkSize)

	// 计算MD5校验和
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		log.Fatalf("Failed to calculate checksum: %v", err)
		return gnet.Shutdown
	}
	s.checksum = hex.EncodeToString(hash.Sum(nil))

	log.Printf("File prepared: size=%d, chunks=%d, checksum=%s",
		s.fileSize, s.totalChunks, s.checksum)
	return gnet.None
}

func (s *gnetUDPSender) OnOpen(c gnet.Conn) (out []byte, action gnet.Action) {
	// 发送元数据
	filename := fmt.Sprintf("%x", md5.Sum([]byte(s.filePath)))
	metadata := FileMetadata{
		Filename:    filename,
		Size:        s.fileSize,
		Checksum:    s.checksum,
		TotalChunks: s.totalChunks,
	}

	// 序列化元数据
	metaBytes := make([]byte, 8+4+4+len(metadata.Filename)+32)
	binary.BigEndian.PutUint64(metaBytes[0:8], uint64(metadata.Size))
	binary.BigEndian.PutUint32(metaBytes[8:12], uint32(len(metadata.Filename)))
	binary.BigEndian.PutUint32(metaBytes[12:16], uint32(metadata.TotalChunks))
	copy(metaBytes[16:16+len(metadata.Filename)], metadata.Filename)
	copy(metaBytes[16+len(metadata.Filename):], metadata.Checksum)

	// 发送元数据包
	if _, err := c.Write(metaBytes); err != nil {
		log.Printf("Failed to send metadata: %v", err)
		return nil, gnet.Close
	}

	// 打开文件读取
	file, err := os.Open(s.filePath)
	if err != nil {
		log.Printf("Failed to open file for reading: %v", err)
		return nil, gnet.Close
	}
	defer file.Close()

	// 分块发送文件
	buffer := make([]byte, UDPChunkSize)
	for sequence := int32(0); sequence < s.totalChunks; sequence++ {
		n, err := file.Read(buffer)
		if err != nil && err != io.EOF {
			log.Printf("Error reading file: %v", err)
			continue
		}
		if n == 0 {
			break
		}

		// 创建UDP数据块
		chunk := make([]byte, 16+n) // 4*4 bytes header + data
		binary.BigEndian.PutUint32(chunk[0:4], uint32(sequence))
		binary.BigEndian.PutUint32(chunk[4:8], uint32(s.totalChunks))
		binary.BigEndian.PutUint32(chunk[8:12], uint32(n))
		var isLastUint32 uint32
		if sequence == s.totalChunks-1 {
			isLastUint32 = 1
		} else {
			isLastUint32 = 0
		}
		binary.BigEndian.PutUint32(chunk[12:16], isLastUint32)
		copy(chunk[16:], buffer[:n])

		// 发送数据块
		if _, err := c.Write(chunk); err != nil {
			log.Printf("Failed to send chunk %d: %v", sequence, err)
			continue
		}

		// UDP发送间隔，避免网络拥塞
		time.Sleep(time.Microsecond * 100)

		if sequence%100 == 0 {
			log.Printf("Sent chunk %d/%d (%d bytes)", sequence, s.totalChunks, n)
		}
	}

	log.Printf("File transfer completed: %d chunks sent", s.totalChunks)
	return nil, gnet.Close
}

func (r *gnetUDPReceiver) OnBoot(eng gnet.Engine) (action gnet.Action) {
	r.receivedMap = make(map[int32]bool)
	r.startTime = time.Now()
	return gnet.None
}

func (r *gnetUDPReceiver) OnTraffic(c gnet.Conn) (action gnet.Action) {
	buf, err := c.Next(-1)
	if err != nil {
		log.Printf("Failed to read data: %v", err)
		return gnet.Close
	}

	// 第一个包是元数据
	if r.file == nil {
		if len(buf) < 8+4+4+32 {
			log.Printf("Invalid metadata packet size: %d", len(buf))
			return gnet.Close
		}

		// 解析元数据
		r.expectedSize = int64(binary.BigEndian.Uint64(buf[0:8]))
		filenameLen := binary.BigEndian.Uint32(buf[8:12])
		r.totalChunks = int32(binary.BigEndian.Uint32(buf[12:16]))

		if len(buf) < 16+int(filenameLen)+32 {
			log.Printf("Invalid metadata packet")
			return gnet.Close
		}

		r.filename = string(buf[16 : 16+filenameLen])
		r.checksum = string(buf[16+filenameLen : 16+filenameLen+32])

		// 创建输出文件
		outputPath := fmt.Sprintf("%s_%s", receivedFilePath, r.filename)
		r.file, err = os.Create(outputPath)
		if err != nil {
			log.Printf("Failed to create file: %v", err)
			return gnet.Close
		}

		log.Printf("Receiving file: %s, size=%d, chunks=%d, checksum=%s",
			r.filename, r.expectedSize, r.totalChunks, r.checksum)
		return gnet.None
	}

	// 处理文件数据块
	for len(buf) >= 16 {
		sequence := int32(binary.BigEndian.Uint32(buf[0:4]))
		// total := int32(binary.BigEndian.Uint32(buf[4:8]))
		chunkSize := int32(binary.BigEndian.Uint32(buf[8:12]))
		isLast := binary.BigEndian.Uint32(buf[12:16]) == 1

		if len(buf) < 16+int(chunkSize) {
			log.Printf("Incomplete chunk data")
			break
		}

		chunkData := buf[16 : 16+chunkSize]

		// 检查是否已接收过此块（处理重复包）
		if !r.receivedMap[sequence] {
			// 写入文件（需要定位到正确的位置）
			offset := int64(sequence) * UDPChunkSize
			if _, err := r.file.WriteAt(chunkData, offset); err != nil {
				log.Printf("Failed to write chunk %d: %v", sequence, err)
				return gnet.Close
			}

			r.receivedMap[sequence] = true
			r.receivedSize += int64(chunkSize)

			// 进度日志
			if sequence%100 == 0 {
				progress := float64(len(r.receivedMap)) / float64(r.totalChunks) * 100
				elapsed := time.Since(r.startTime).Seconds()
				speed := float64(r.receivedSize) / elapsed / (1024 * 1024) // MB/s
				log.Printf("Progress: %.2f%% (%d/%d chunks), Speed: %.2f MB/s",
					progress, len(r.receivedMap), r.totalChunks, speed)
			}
		}

		buf = buf[16+chunkSize:]

		// 如果是最后一块，检查传输是否完成
		if isLast {
			receivedCount := len(r.receivedMap)
			if receivedCount >= int(r.totalChunks) {
				log.Printf("File transfer completed: %d chunks received", receivedCount)
				return gnet.Close
			}
		}
	}

	return gnet.None
}

func (r *gnetUDPReceiver) OnClose(c gnet.Conn, err error) (action gnet.Action) {
	if r.file != nil {
		r.file.Sync()

		// 验证校验和
		r.file.Seek(0, 0)
		hash := md5.New()
		if _, err := io.Copy(hash, r.file); err == nil {
			actualChecksum := hex.EncodeToString(hash.Sum(nil))
			if actualChecksum == r.checksum {
				log.Printf("File integrity verified: checksum matches")
			} else {
				log.Printf("File integrity check failed: expected %s, got %s",
					r.checksum, actualChecksum)
			}
		}

		r.file.Close()
	}
	return gnet.None
}

func (r *gnetUDPReceiver) OnShutdown(eng gnet.Engine) {
	if r.file != nil {
		r.file.Close()
	}
}

func TestGnetSender(t *testing.T) {
	sender := &gnetUDPSender{
		filePath: testFilePath,
		targets:  []string{"udp://192.168.1.103:3400", "udp://192.168.1.103:3401"},
	}

	client, err := gnet.NewClient(sender)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Start the client to initialize event loops
	if err = client.Start(); err != nil {
		t.Fatalf("Failed to start client: %v", err)
	}
	defer client.Stop()

	for _, tar := range sender.targets {
		go client.Dial("udp", tar[6:])
	}

	// 等待传输完成
	time.Sleep(30 * time.Second)
}

func TestGnetReceiver(t *testing.T) {
	receiver := &gnetUDPReceiver{}

	addrs := []string{"udp://:3400", "udp://:3401"}
	err := gnet.Rotate(receiver, addrs)

	if err != nil {
		t.Fatalf("Failed to start receiver: %v", err)
		panic(err)
	}

	// 保持接收端运行
	time.Sleep(60 * time.Second)
}
