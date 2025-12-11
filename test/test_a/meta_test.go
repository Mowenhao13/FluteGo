package test

// import (
// 	"FluteGo/constant"
// 	filedesc "FluteGo/pkg/filedesc"
// 	meta "FluteGo/pkg/meta"
// 	oti "FluteGo/pkg/oti"
// 	pool "FluteGo/pkg/pool"
// 	utils "FluteGo/pkg/utils"
// 	"math/rand"
// 	"time"

// 	"os"
// 	"runtime"
// 	"runtime/pprof"
// 	"testing"
// )

// func TestSendMetaPkt(t *testing.T) {
// 	const (
// 		fdtID       = 1
// 		sendPath    = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1024mb.bin"
// 		saveDir     = "/home/Halllo/Projects/Flute_test_v2/cmd/received_files"
// 		name        = "test_1024mb.bin"
// 		transferLen = 1024 * 1024 * 1024
// 		contentType = "application/octet-stream"
// 		md5         = "cd573cfaace07e7949bc0c46028904ff"
// 	)
// 	fd := filedesc.FileDesc{
// 		FdtID:       fdtID,
// 		SendPath:    sendPath,
// 		SaveDir:     saveDir,
// 		Name:        name,
// 		TransferLen: transferLen,
// 		ContentType: contentType,
// 		Md5:         md5,
// 	}
// 	oti := oti.NewReedSolomon(240, 16)
// 	basePort := 3400
// 	numPorts := 20
// 	metaPkt := meta.MetaPkt{
// 		File:             &fd,
// 		Oti:              oti,
// 		BasePort:         basePort,
// 		NumPorts:         uint16(numPorts),
// 		MaxPacketSize:    constant.MaxPacketSize,
// 		TotalFiles:       1,
// 		CurrentFileIndex: 1,
// 	}

// 	// 创建内存profile文件
// 	memProfile, err := os.Create("send_metapkt_mem_profile.pprof")
// 	if err != nil {
// 		t.Fatalf("Failed to create memory profile: %v", err)
// 	}
// 	defer memProfile.Close()

// 	// 在测试开始时获取内存快照
// 	runtime.GC()
// 	if err := pprof.WriteHeapProfile(memProfile); err != nil {
// 		t.Fatalf("Failed to write initial heap profile: %v", err)
// 	}

// 	// 记录开始时的内存状态
// 	var memStatsStart, memStatsEnd runtime.MemStats
// 	runtime.ReadMemStats(&memStatsStart)

// 	globalPool := pool.GetGlobalPool()
// 	if globalPool == nil {
// 		t.Fatalf("Pool not initialized\n")
// 	}

// 	udpConn, err := globalPool.GetGlobalConnection(destIP, port)
// 	if err != nil {
// 		t.Fatalf("Failed to get the connection\n")
// 	}
// 	defer globalPool.ReturnConnection(udpConn)
// 	conn := udpConn.Conn

// 	pktData := metaPkt.Serialize()
// 	writeBytes, err := conn.Write(pktData)
// 	if err != nil {
// 		t.Log("Error occur: ", err)
// 	}

// 	t.Logf("Sent %d bytes", writeBytes)
// 	runtime.ReadMemStats(&memStatsEnd)

// 	// 写入最终的内存profile
// 	if err := pprof.WriteHeapProfile(memProfile); err != nil {
// 		t.Fatalf("Failed to write final heap profile: %v", err)
// 	}

// 	// 输出详细的内存分析结果
// 	t.Logf("=== 内存性能分析结果 ===")
// 	t.Logf("总分配内存: %v bytes", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
// 	t.Logf("峰值堆内存: %v bytes, %v MB", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
// 	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
// 	t.Logf("内存分配次数: %v", memStatsEnd.Mallocs-memStatsStart.Mallocs)
// 	t.Logf("堆对象数量: %v", memStatsEnd.HeapObjects)
// }

// func TestRecvMetaPkt(t *testing.T) {
// 	// 创建内存profile文件
// 	memProfile, err := os.Create("recv_metapkt_mem_profile.pprof")
// 	if err != nil {
// 		t.Fatalf("Failed to create memory profile: %v", err)
// 	}
// 	defer memProfile.Close()

// 	// 在测试开始时获取内存快照
// 	runtime.GC()
// 	if err := pprof.WriteHeapProfile(memProfile); err != nil {
// 		t.Fatalf("Failed to write initial heap profile: %v", err)
// 	}

// 	// 记录开始时的内存状态
// 	var memStatsStart, memStatsEnd runtime.MemStats
// 	runtime.ReadMemStats(&memStatsStart)

// 	conn, err := utils.CreateUDPListener("192.168.1.103:3399")
// 	if err != nil {
// 		t.Log("Error: ", err)
// 	}

// 	// conn.SetReadDeadline(time.Now().Add(5 * time.Second))
// 	buf := make([]byte, 1500)

// 	for {
// 		n, _, err := conn.ReadFromUDP(buf)
// 		if err != nil {
// 			t.Log("Error: ", err)
// 		}

// 		pktData := buf[:n]
// 		metaPkt, err := meta.DeserializeMetaPkt(pktData)
// 		if err != nil {
// 			t.Log("Error: ", err)
// 		}

// 		metaPkt.ShowPktInfo()
// 		break
// 	}

// 	runtime.ReadMemStats(&memStatsEnd)

// 	// 写入最终的内存profile
// 	if err := pprof.WriteHeapProfile(memProfile); err != nil {
// 		t.Fatalf("Failed to write final heap profile: %v", err)
// 	}

// 	// 输出详细的内存分析结果
// 	t.Logf("=== 内存性能分析结果 ===")
// 	t.Logf("总分配内存: %v bytes", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
// 	t.Logf("峰值堆内存: %v bytes, %v MB", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
// 	t.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
// 	t.Logf("内存分配次数: %v", memStatsEnd.Mallocs-memStatsStart.Mallocs)
// 	t.Logf("堆对象数量: %v", memStatsEnd.HeapObjects)
// }

// func TestInitMetaPkt(t *testing.T) {
// 	const (
// 		fileDir = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/"
// 		saveDir = "/home/Halllo/Projects/Flute_test_v2/cmd/received_files"
// 	)

// 	files, err := os.ReadDir(fileDir)
// 	if err != nil {
// 		t.Fatalf("Failed to read directory: %v", err)
// 	}

// 	var fileList []*os.File
// 	for _, file := range files {
// 		if !file.IsDir() {
// 			f, err := os.Open(fileDir + file.Name())
// 			if err != nil {
// 				t.Fatalf("Failed to open file: %v", err)
// 			}
// 			defer f.Close()
// 			fileList = append(fileList, f)
// 		}
// 	}

// 	oti := oti.NewReedSolomon(240, 16)

// 	fdtID := uint8(0)
// 	for _, file := range fileList {
// 		metaPkt, err := meta.InitMetaPkt(file, oti, 3400, 20, fdtID, saveDir)
// 		if err != nil {
// 			t.Fatalf("Failed to init MetaPkt: %v", err)
// 		}
// 		metaPkt.TotalFiles = uint16(len(fileList))
// 		metaPkt.CurrentFileIndex = uint16(fdtID + 1)
// 		t.Logf("Initialized MetaPkt for file: %s, FdtID: %d", metaPkt.File.Name, metaPkt.File.FdtID)
// 		fdtID++

// 		metaPkt.ShowPktInfo()
// 	}
// }

// // Starting pprof server on :6060
// // goos: linux
// // goarch: amd64
// // pkg: FluteGo/test
// // cpu: AMD Ryzen 9 7940HX with Radeon Graphics
// // === RUN   BenchmarkInitMetaPkt
// // BenchmarkInitMetaPkt
// //     /home/Halllo/Projects/Flute_test_v2/test/meta_test.go:243: Initialized MetaPkt for file: test_1024mb.bin, FdtID: 0
// // FdtID: 0
// // File name: test_1024mb.bin
// // File transfer len: 1073741824
// // File content type: application/octet-stream
// // File md5sum: cd573cfaace07e7949bc0c46028904ff
// // File send path: /home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1024mb.bin
// // File save dir: /home/Halllo/Projects/Flute_test_v2/cmd/received_files
// // Oti id: 2
// // BasePort: 3400
// // NumPort: 20
// // MaxPacketSize: 1400
// //     /home/Halllo/Projects/Flute_test_v2/test/meta_test.go:243: Initialized MetaPkt for file: test_1mb.bin, FdtID: 1
// // FdtID: 1
// // File name: test_1mb.bin
// // File transfer len: 1048576
// // File content type: application/octet-stream
// // File md5sum: b6d81b360a5672d80c27430f39153e2c
// // File send path: /home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1mb.bin
// // File save dir: /home/Halllo/Projects/Flute_test_v2/cmd/received_files
// // Oti id: 2
// // BasePort: 3400
// // NumPort: 20
// // MaxPacketSize: 1400
// //     /home/Halllo/Projects/Flute_test_v2/test/meta_test.go:243: Initialized MetaPkt for file: test_50mb.bin, FdtID: 2
// // FdtID: 2
// // File name: test_50mb.bin
// // File transfer len: 52428800
// // File content type: application/octet-stream
// // File md5sum: 25e317773f308e446cc84c503a6d1f85
// // File send path: /home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_50mb.bin
// // File save dir: /home/Halllo/Projects/Flute_test_v2/cmd/received_files
// // Oti id: 2
// // BasePort: 3400
// // NumPort: 20
// // MaxPacketSize: 1400
// //     /home/Halllo/Projects/Flute_test_v2/test/meta_test.go:257: === 内存性能分析结果 ===
// //     /home/Halllo/Projects/Flute_test_v2/test/meta_test.go:258: 总分配内存: 119648 bytes
// //     /home/Halllo/Projects/Flute_test_v2/test/meta_test.go:259: 峰值堆内存: 1906096 bytes, 1 MB
// //     /home/Halllo/Projects/Flute_test_v2/test/meta_test.go:260: 垃圾回收次数: 0
// //     /home/Halllo/Projects/Flute_test_v2/test/meta_test.go:261: 内存分配次数: 125
// //     /home/Halllo/Projects/Flute_test_v2/test/meta_test.go:262: 堆对象数量: 1142
// // BenchmarkInitMetaPkt-32                1        1171835289 ns/op         2711656 B/op        768 allocs/op
// // PASS
// // ok      FluteGo/test    1.276s

// func BenchmarkInitMetaPkt(b *testing.B) {
// 	// 创建内存profile文件
// 	memProfile, err := os.Create("init_metapkt_mem_profile.pprof")
// 	if err != nil {
// 		b.Fatalf("Failed to create memory profile: %v", err)
// 	}
// 	defer memProfile.Close()

// 	// 在测试开始时获取内存快照
// 	runtime.GC()
// 	if err := pprof.WriteHeapProfile(memProfile); err != nil {
// 		b.Fatalf("Failed to write initial heap profile: %v", err)
// 	}

// 	// 记录开始时的内存状态
// 	var memStatsStart, memStatsEnd runtime.MemStats
// 	runtime.ReadMemStats(&memStatsStart)

// 	const (
// 		fileDir = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/"
// 		saveDir = "/home/Halllo/Projects/Flute_test_v2/cmd/received_files"
// 	)

// 	files, err := os.ReadDir(fileDir)
// 	if err != nil {
// 		b.Fatalf("Failed to read directory: %v", err)
// 	}

// 	var fileList []*os.File
// 	for _, file := range files {
// 		if !file.IsDir() {
// 			f, err := os.Open(fileDir + file.Name())
// 			if err != nil {
// 				b.Fatalf("Failed to open file: %v", err)
// 			}
// 			defer f.Close()
// 			fileList = append(fileList, f)
// 		}
// 	}

// 	oti := oti.NewReedSolomon(240, 16)

// 	fdtID := uint8(0)
// 	for _, file := range fileList {
// 		metaPkt, err := meta.InitMetaPkt(file, oti, 3400, 20, fdtID, saveDir)
// 		if err != nil {
// 			b.Fatalf("Failed to init MetaPkt: %v", err)
// 		}
// 		metaPkt.TotalFiles = uint16(len(fileList))
// 		metaPkt.CurrentFileIndex = uint16(fdtID + 1)
// 		b.Logf("Initialized MetaPkt for file: %s, FdtID: %d", metaPkt.File.Name, metaPkt.File.FdtID)
// 		fdtID++

// 		metaPkt.ShowPktInfo()
// 	}

// 	runtime.ReadMemStats(&memStatsEnd)

// 	// 写入最终的内存profile
// 	if err := pprof.WriteHeapProfile(memProfile); err != nil {
// 		b.Fatalf("Failed to write final heap profile: %v", err)
// 	}

// 	// 输出详细的内存分析结果
// 	b.Logf("=== 内存性能分析结果 ===")
// 	b.Logf("总分配内存: %v bytes", memStatsEnd.TotalAlloc-memStatsStart.TotalAlloc)
// 	b.Logf("峰值堆内存: %v bytes, %v MB", memStatsEnd.HeapAlloc, memStatsEnd.HeapAlloc/(1024*1024))
// 	b.Logf("垃圾回收次数: %v", memStatsEnd.NumGC-memStatsStart.NumGC)
// 	b.Logf("内存分配次数: %v", memStatsEnd.Mallocs-memStatsStart.Mallocs)
// 	b.Logf("堆对象数量: %v", memStatsEnd.HeapObjects)
// }

// func getRandomOti() oti.Oti {
// 	schemes := []func() oti.Oti{
// 		func() oti.Oti { return oti.NewNoCode(uint16(64 + rand.Intn(192))) },   // 64-256字节
// 		func() oti.Oti { return oti.NewRaptorQ(uint16(128 + rand.Intn(128))) }, // 128-256字节
// 		func() oti.Oti {
// 			dataShards := uint8(1 + rand.Intn(10))  // 1-10个数据分片
// 			parityShards := uint8(1 + rand.Intn(5)) // 1-5个校验分片
// 			return oti.NewReedSolomon(dataShards, parityShards)
// 		},
// 	}

// 	return schemes[rand.Intn(len(schemes))]()
// }

// // 创建临时文件辅助函数
// func createTempFile() *os.File {
// 	// 确保tmp目录存在
// 	os.MkdirAll("./tmp", 0755)

// 	// 创建临时文件
// 	tempFile, err := os.CreateTemp("./tmp", "test_*.dat")
// 	if err != nil {
// 		panic("无法创建临时文件: " + err.Error())
// 	}

// 	// 生成随机文件内容（1KB - 1MB）
// 	fileSize := 1024 + rand.Intn(1024*1024-1024)
// 	content := make([]byte, fileSize)
// 	rand.Read(content)

// 	// 写入文件
// 	if _, err := tempFile.Write(content); err != nil {
// 		tempFile.Close()
// 		panic("无法写入临时文件: " + err.Error())
// 	}

// 	// 重置文件指针到开头
// 	tempFile.Seek(0, 0)

// 	return tempFile
// }

// func RandomMetaPkt() *meta.MetaPkt {
// 	rand.Seed(time.Now().UnixNano())

// 	// 创建临时文件
// 	file := createTempFile()
// 	defer file.Close()

// 	// 生成随机 FileDesc
// 	fdtID := uint8(rand.Intn(256))
// 	saveDir := "./tmp"

// 	fileDesc, err := filedesc.GetFileDesc(file, fdtID, saveDir)
// 	if err != nil {
// 		panic("无法获取文件描述符: " + err.Error())
// 	}

// 	// 随机选择 FEC 编码方案
// 	oti := getRandomOti()

// 	// 随机生成端口配置
// 	basePort := 10000 + rand.Intn(20000)
// 	numPorts := uint16(1 + rand.Intn(10))
// 	maxPacketSize := uint16(512 + rand.Intn(1472))

// 	return &meta.MetaPkt{
// 		File:             fileDesc,
// 		Oti:              oti,
// 		BasePort:         basePort,
// 		NumPorts:         numPorts,
// 		MaxPacketSize:    maxPacketSize,
// 		TotalFiles:       1,
// 		CurrentFileIndex: 1,
// 	}
// }
