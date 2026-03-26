package main

import (
	"FluteGo/constant"
	"FluteGo/pkg/apiserver"
	"FluteGo/pkg/config"
	"FluteGo/pkg/meta"
	"FluteGo/pkg/oti"
	"FluteGo/pkg/pool"
	sender "FluteGo/pkg/sender"
	"FluteGo/pkg/sock"
	"FluteGo/pkg/web"
	"context"
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

		// Save uploaded data to sendFileDir.
		if err := os.MkdirAll(cfg.Sender.SendFileDir, 0755); err != nil {
			return 0, fmt.Errorf("cannot create upload dir: %w", err)
		}
		savePath := filepath.Join(cfg.Sender.SendFileDir, filepath.Base(fileName))
		f, err := os.Create(savePath)
		if err != nil {
			return 0, fmt.Errorf("cannot create file: %w", err)
		}
		if _, err := io.Copy(f, data); err != nil {
			f.Close()
			return 0, fmt.Errorf("write error: %w", err)
		}
		f.Close()

		// Re-open for reading.
		f, err = os.Open(savePath)
		if err != nil {
			return 0, fmt.Errorf("cannot open saved file: %w", err)
		}

		// Allocate fdtID and port range atomically.
		fid := uint8(nextFdtID.Add(1))
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
			defer p.CloseFileConn(fid)
			defer f.Close()
			if err := SendFile(p, metaPkt, limiter, nil, nil, 1, srv); err != nil {
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

func SendFile(p *pool.ConnPool, mt *meta.MetaPkt, limiter *rate.Limiter, onOverhead func(int64), onProgress func(int64), maxConcurrentSends int, srv *apiserver.Server) error {
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

	s, err := sender.InitSender(mt, limiter, maxConcurrentSends)
	if err != nil {
		return fmt.Errorf("Failed to init sender: %v", err)
	}

	if srv != nil {
		totalBytes := s.GetTotalBytesToSend()
		fecType := senderFECTypeName(mt.Oti.FECEncodingID)
		srv.State().RegisterFile("sender", mt.File.FdtID, mt.File.Name,
			int64(mt.File.TransferLen), fecType, mt.File.Md5)
		stateAdapter := srv.State().SenderProgressAdapter(mt.File.FdtID, totalBytes)
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
