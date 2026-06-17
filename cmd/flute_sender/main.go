package main

import (
	"FluteGo/constant"
	"FluteGo/pkg/apiserver"
	"FluteGo/pkg/config"
	"FluteGo/pkg/encoder"
	"FluteGo/pkg/meta"
	"FluteGo/pkg/oti"
	"FluteGo/pkg/pool"
	sender "FluteGo/pkg/sender"
	"FluteGo/pkg/sock"
	"FluteGo/pkg/web"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/time/rate"
)

var (
	globalPool *pool.ConnPool
	poolMu     sync.Mutex
	poolDestIP string
)

var (
	currentDestIP   string
	currentDestIPMu sync.RWMutex
)

// nextFdtID and nextPort are global atomic counters shared across API sends.
var nextFdtID atomic.Uint32
var nextPort  atomic.Int32

// sendSem 串行发送信号量，确保一次只发送一个文件
var sendSem = make(chan struct{}, 1)

func ensurePool(destIP string) error {
	poolMu.Lock()
	defer poolMu.Unlock()
	if globalPool != nil && poolDestIP == destIP {
		return nil
	}
	if globalPool != nil {
		globalPool.CloseMetaConn()
	}
	pool.InitConnPool(destIP, 0)
	p := pool.GetConnPool()
	if p == nil {
		return fmt.Errorf("pool init failed")
	}
	if _, err := p.InitMetaConn(); err != nil {
		return err
	}
	globalPool = p
	poolDestIP = destIP
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start() //nolint:errcheck
}

func main() {
	// --- CLI mode flags ---
	cliMode := flag.Bool("cli", false, "Run in CLI mode (no JSON config, no API server)")
	destIPFlag := flag.String("dest-ip", "", "Destination IP address (required in CLI mode)")
	filePathFlag := flag.String("file", "", "File to send (required in CLI mode)")
	fecTypeFlag := flag.String("fec", "RaptorQ", "FEC type: NoCode, RaptorQ, ReedSolomon")
	fdtIDFlag := flag.Int("fdt-id", 1, "File transfer ID (1-255, change per test to reuse receiver)")
	maxPacketSizeFlag := flag.Int("max-packet-size", 1408, "Maximum UDP packet size")
	baseFilePortFlag := flag.Int("base-file-port", 3400, "Base file transfer port")
	metaPortFlag := flag.Int("meta-port", 3399, "Meta port")
	numPortsFlag := flag.Int("num-ports", 1, "Number of transfer ports")
	sendFileDirFlag := flag.String("send-file-dir", "cmd/send_files/", "Directory for sent files")
	sendRedundancyRatioFlag := flag.Float64("send-redundancy-ratio", 1.05, "Redundancy ratio")
	rateLimitMbpsFlag := flag.Int("rate-limit-mbps", 500, "Rate limit in Mbps")
	percentageFlag := flag.Int("percentage", 100, "Send percentage of total data (1-100, for loss recovery testing)")
	startSendWaitFlag := flag.Int("start-send-wait", 1, "Seconds to wait before sending")
	csvFlag := flag.Bool("csv", false, "Save transfer results to CSV file alongside the sent file")
	flag.Parse()

	if *cliMode {
	runCLISender(*destIPFlag, *filePathFlag, *fecTypeFlag, uint8(*fdtIDFlag),
			*maxPacketSizeFlag, *baseFilePortFlag, *metaPortFlag, *numPortsFlag,
			*sendFileDirFlag, *sendRedundancyRatioFlag, *rateLimitMbpsFlag, *percentageFlag, *startSendWaitFlag,
			*csvFlag)
		return
	}

	cfg, err := config.Load("config_sender.json")
	if err != nil {
		log.Printf("[config] load error: %v, using defaults", err)
		cfg = config.Default()
	}

	// Signal context for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Initialize currentDestIP from config.
	currentDestIPMu.Lock()
	currentDestIP = cfg.DestIP
	currentDestIPMu.Unlock()

	// Global rate limiter.
	limiter, _ := sender.CreateRateLimiter(float64(cfg.Sender.RateLimitMbps), 1500)

	// Initialize atomic counters.
	nextFdtID.Store(0)
	nextPort.Store(int32(cfg.Network.BaseFilePort))

	// Set up API server.
	srv := apiserver.New(cfg.Server.Port, "sender").
		WithDestIP(cfg.DestIP).
		WithFilePort(cfg.Network.BaseFilePort)

	// Register setDestFn — lets the frontend update the destination IP at runtime.
	srv.SetDestFunc(func(ip string) error {
		currentDestIPMu.Lock()
		currentDestIP = ip
		currentDestIPMu.Unlock()
		srv.WithDestIP(ip)
		return nil
	})

	// Register send function.
	srv.SetSendFunc(func(fileName string, data io.Reader, fecType string) (uint8, error) {
		currentDestIPMu.RLock()
		destIP := currentDestIP
		currentDestIPMu.RUnlock()

		if err := ensurePool(destIP); err != nil {
			return 0, fmt.Errorf("pool: %w", err)
		}

		// Capture pool pointer while holding the lock.
		poolMu.Lock()
		p := globalPool
		poolMu.Unlock()

		// Save uploaded data to system temp dir, send完后删除
		fid := uint8(nextFdtID.Add(1))
		savePath := filepath.Join(os.TempDir(), filepath.Base(fileName))
		f, err := os.Create(savePath)
		if err != nil {
			return 0, fmt.Errorf("cannot create file: %w", err)
		}
		if _, err := io.Copy(f, data); err != nil {
			f.Close()
			os.Remove(savePath)
			return 0, fmt.Errorf("write error: %w", err)
		}
		f.Close()

		// Re-open for reading.
		f, err = os.Open(savePath)
		if err != nil {
			return 0, fmt.Errorf("cannot open saved file: %w", err)
		}

		// Port range atomically allocated (fid already allocated above).
		portBase := int(nextPort.Add(int32(cfg.Network.NumPorts))) - cfg.Network.NumPorts

		// Build OTI.
		o := fecTypeToOti(fecType)

		// Create connections.
		numPorts := uint8(cfg.Network.NumPorts)
		conns, connErrs := p.CreateFileConn(fid, numPorts, portBase)
		for _, cErr := range connErrs {
			if cErr != nil {
				log.Printf("[sendFn] conn error for fdtID %d: %v", fid, cErr)
			}
		}
		if len(conns) == 0 {
			f.Close()
			return 0, fmt.Errorf("no connections available for fdtID %d", fid)
		}

		// Build MetaPkt.
		metaPkt, err := meta.InitMetaPkt(f, o, portBase, uint16(numPorts), fid)
		if err != nil {
			f.Close()
			p.CloseFileConn(fid)
			return 0, fmt.Errorf("meta init error: %w", err)
		}

		// Async send (does not block the HTTP handler).
		go func() {
			sendSem <- struct{}{}
			defer func() { <-sendSem }()
			defer p.CloseFileConn(fid)
			defer f.Close()
				defer os.Remove(savePath)
			if err := SendFile(p, metaPkt, limiter, nil, nil, 1, srv, cfg.Sender.SendRedundancyRatio, *csvFlag); err != nil {
				log.Printf("[sendFn] SendFile error fdtID %d: %v", fid, err)
			}
		}()

		return fid, nil
	})
	srv.WithUploadDir(cfg.Sender.SendFileDir)
	srv.WithStaticContent(web.HTML)

	// Shutdown: close pool.
	defer func() {
		poolMu.Lock()
		if globalPool != nil {
			globalPool.CloseMetaConn()
		}
		poolMu.Unlock()
	}()

	if cfg.Server.Enabled {
		go srv.Start(ctx) //nolint:errcheck
		time.Sleep(300 * time.Millisecond)
		openBrowser(fmt.Sprintf("http://localhost:%d", cfg.Server.Port))
	}

	// Wait for shutdown signal.
	<-ctx.Done()
	log.Println("Exit program")
}

func sendData(p *pool.ConnPool, wsck *sock.MsSocket, data []byte) error {
	if p == nil {
		return fmt.Errorf("pool not initialized")
	}
	destIPLocal := strings.TrimSpace(p.DestIP)
	if destIPLocal == "" {
		return fmt.Errorf("destination IP address not set")
	}
	ip := net.ParseIP(destIPLocal)
	if ip == nil {
		return fmt.Errorf("invalid IP address: %s", destIPLocal)
	}
	destAddr := &net.UDPAddr{
		IP:   ip,
		Port: constant.META_PORT,
	}
	_, err := wsck.Socket.WriteToUDP(data, destAddr)
	return err
}

func SendFile(p *pool.ConnPool, mt *meta.MetaPkt, limiter *rate.Limiter, onOverhead func(int64), onProgress func(int64), maxConcurrentSends int, srv *apiserver.Server, redundancyRatio float64, csvEnabled bool) error {
	metaConn, err := p.GetMetaConn()
	if err != nil {
		return err
	}

	metaData := mt.Serialize()

	log.Printf("[SendFile] Meta connection: %s:%d (Mode: %d, FdtID: %d)",
		p.DestIP, constant.META_PORT, p.Mode, metaConn.FdtID)
	log.Printf("[SendFile] Sending metadata: %d bytes to %s:%d", len(metaData), p.DestIP, constant.META_PORT)

	if err := sendData(p, metaConn, metaData); err != nil {
		log.Printf("[SendFile] Failed to send metadata: %v", err)
		return err
	}

	log.Printf("[SendFile] Metadata sent successfully")
	log.Printf("Sender will be started after %d seconds\n", constant.START_SEND_WAIT)
	time.Sleep(constant.START_SEND_WAIT * time.Second)

	s, err := sender.InitSender(mt, limiter, maxConcurrentSends, redundancyRatio)
	if err != nil {
		return fmt.Errorf("Failed to init sender: %v", err)
	}

	if srv != nil {
		totalBytes := s.GetTotalBytesToSend()
		fecType := senderFECTypeName(mt.Oti.FECEncodingID)
		srv.State().RegisterFile("sender", mt.File.FdtID, mt.File.Name,
			int64(mt.File.TransferLen), fecType, mt.File.Md5)
		stateAdapter := srv.State().SenderProgressAdapter(mt.File.FdtID, int64(mt.File.TransferLen), totalBytes)
		if onProgress != nil {
			orig := onProgress
			onProgress = func(sent int64) {
				orig(sent)
				stateAdapter(sent)
			}
		} else {
			onProgress = stateAdapter
		}
	}

	if onOverhead != nil {
		totalBytes := s.GetTotalBytesToSend()
		overhead := totalBytes - int64(mt.File.TransferLen)
		onOverhead(overhead)
	}

	if onProgress != nil {
		s.SetProgressCallback(onProgress)
	}

	s.CSVEnabled = csvEnabled
	return s.Start(context.Background())
}

// fecTypeToOti converts a FEC type string to an OTI instance.
func fecTypeToOti(fecType string) oti.Oti {
	switch fecType {
	case "RaptorQ":
		return oti.NewRaptorQ(1400)
	case "ReedSolomon":
		return oti.NewReedSolomon(12, 4)
	default:
		return oti.NewNoCode(1400)
	}
}

// senderFECTypeName maps a FECEncodingID to a human-readable string.
func senderFECTypeName(id uint8) string {
	switch id {
	case 0:
		return "NoCode"
	case 1:
		return "RaptorQ"
	case 2:
		return "ReedSolomon"
	default:
		return "Unknown"
	}
}

// runCLISender runs the sender in CLI mode (no JSON config, no API server).
func runCLISender(destIP, filePath, fecType string, fid uint8,
	maxPacketSize, baseFilePort, metaPort, numPorts int,
	sendFileDir string, sendRedundancyRatio float64,
	rateLimitMbps, percentage, startSendWait int, csvEnabled bool) {

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("[CLI] ===== Sender CLI Mode =====")
	log.Printf("[CLI] Target: %s, File: %s, FEC: %s, Ratio: %.2f, Rate: %d Mbps",
		destIP, filePath, fecType, sendRedundancyRatio, rateLimitMbps)

	// Validate required parameters
	if destIP == "" {
		log.Fatal("[CLI] --dest-ip is required")
	}
	if filePath == "" {
		log.Fatal("[CLI] --file is required")
	}

	// Check file exists
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		log.Fatalf("[CLI] Cannot access file %s: %v", filePath, err)
	}
	fileSizeMB := float64(fileInfo.Size()) / (1024 * 1024)
	log.Printf("[CLI] File: %s, Size: %d bytes (%.2f MB)", filepath.Base(filePath), fileInfo.Size(), fileSizeMB)

	// Build OTI from FEC type
	// Note: SymbolSize = maxPacketSize - 8 (8 bytes for seqNum header)
	payloadSize := uint16(maxPacketSize - 8)
	var o oti.Oti
	switch fecType {
	case "RaptorQ":
		o = oti.NewRaptorQ(payloadSize)
	case "ReedSolomon":
		o = oti.NewReedSolomon(12, 4)
	default:
		o = oti.NewNoCode(payloadSize)
	}
	log.Printf("[CLI] OTI: FEC=%d, Symbol=%d (payload), Chunk=%d",
		o.FECEncodingID, o.SymbolSize, o.MaximumChunkSize)

	// Init connection pool (sender mode)
	pool.InitConnPool(destIP, constant.POOL_SEND)
	p := pool.GetConnPool()
	if p == nil {
		log.Fatal("[CLI] Failed to initialize connection pool")
	}
	_, metaInitErr := p.InitMetaConn()
	if metaInitErr != nil {
		log.Fatalf("[CLI] Failed to init meta connection: %v", metaInitErr)
	}
	defer func() {
		p.CloseMetaConn()
		p.CloseAllConns()
	}()

	// Create file connections
	// fid from CLI --fdt-id flag
	conns, connErrs := p.CreateFileConn(fid, uint8(numPorts), baseFilePort)
	for _, cerr := range connErrs {
		if cerr != nil {
			log.Printf("[CLI] Connection warning: %v", cerr)
		}
	}
	if len(conns) == 0 {
		log.Fatal("[CLI] No file connections available")
	}
	defer p.CloseFileConn(fid)

	// Build MetaPkt
	f, openErr := os.Open(filePath)
	if openErr != nil {
		log.Fatalf("[CLI] Cannot open file: %v", openErr)
	}
	mt, metaErr := meta.InitMetaPkt(f, o, baseFilePort, uint16(numPorts), fid)
	f.Close()
	if metaErr != nil {
		log.Fatalf("[CLI] Failed to build MetaPkt: %v", metaErr)
	}

	// Send MetaPkt to receiver
	metaConn, mcErr := p.GetMetaConn()
	if mcErr != nil {
		log.Fatalf("[CLI] Failed to get meta connection: %v", mcErr)
	}
	metaData := mt.Serialize()
	destAddr := &net.UDPAddr{IP: net.ParseIP(destIP), Port: metaPort}
	log.Printf("[CLI] Sending MetaPkt (%d bytes) to %s:%d ...", len(metaData), destIP, metaPort)
	if _, wErr := metaConn.Socket.WriteToUDP(metaData, destAddr); wErr != nil {
		log.Fatalf("[CLI] Failed to send MetaPkt: %v", wErr)
	}
	log.Printf("[CLI] MetaPkt sent. Waiting %d seconds before data...", startSendWait)
	time.Sleep(time.Duration(startSendWait) * time.Second)

	// Build encoder config WITH overridden redundancy ratio
	chunkSize := mt.Oti.MaximumChunkSize
	if chunkSize == 0 {
		chunkSize = uint32(constant.DefaultChunkSize)
	}
	config := encoder.EncoderConfig{
		Type:            encoder.EncoderType(mt.Oti.FECEncodingID),
		FileSize:        mt.File.TransferLen,
		ChunkSize:       chunkSize,
		SymbolSize:      mt.Oti.SymbolSize,
		DataShards:      uint16(mt.Oti.DataShards),
		ParityShards:    uint16(mt.Oti.ParityShards),
		RedundancyRatio: sendRedundancyRatio, // From CLI flag, not constant!
		MaxPacketSize:   uint16(maxPacketSize),
	}
	log.Printf("[CLI] Encoder config: ratio=%.2f, chunk=%d, symbol=%d",
		config.RedundancyRatio, config.ChunkSize, config.SymbolSize)

	// Create rate limiter
	limiter, _ := sender.CreateRateLimiter(float64(rateLimitMbps), maxPacketSize)

	// Create sender via NewSender (bypasses InitSender's constant-based config)
	s, sErr := sender.NewSender(filePath, config, fid, 1, limiter, runtime.NumCPU())
	if sErr != nil {
		log.Fatalf("[CLI] Failed to create sender: %v", sErr)
	}

	// 设置发送百分比（用于丢包恢复测试）
	s.SetSendPercentage(int32(percentage))
	if percentage != 100 {
		log.Printf("[CLI] Percentage mode: sending %d%% of total data (simulating %.0f%% loss)", percentage, 100.0-float64(percentage))
	}

	// Progress callback - print at every percent change
	totalToSend := s.GetTotalBytesToSend()
	fileSize := int64(mt.File.TransferLen)
	var lastPct int64 = -1
	s.SetProgressCallback(func(sent int64) {
		// 将 wire 字节转换为 payload 字节，使进度与接收端可比
		var payloadBytes int64
		if sent >= totalToSend {
			payloadBytes = fileSize
		} else if totalToSend > 0 && fileSize > 0 {
			payloadBytes = int64(float64(sent) * float64(fileSize) / float64(totalToSend))
		} else {
			payloadBytes = sent
		}
		if payloadBytes > fileSize {
			payloadBytes = fileSize
		}
		pct := payloadBytes * 100 / fileSize
		if pct != lastPct {
			lastPct = pct
			log.Printf("[CLI] Sending: %d%% (%d/%d bytes)", pct, payloadBytes, fileSize)
		}
	})

	// Start sending
	log.Printf("[CLI] Starting data transmission (ratio=%.2f, rate=%dMbps)...", sendRedundancyRatio, rateLimitMbps)
	s.CSVEnabled = csvEnabled
	sendStart := time.Now()
	if err := s.Start(context.Background()); err != nil {
		log.Fatalf("[CLI] Send error: %v", err)
	}
	duration := time.Since(sendStart)
	// 使用 payload 字节计算速率，与接收端保持一致
	mbps := (float64(fileSize) * 8.0 / duration.Seconds()) / 1e6
	log.Printf("[CLI] ===== SEND COMPLETE =====")
	log.Printf("[CLI] Duration: %v, Throughput (payload): %.2f Mbps", duration, mbps)
}
