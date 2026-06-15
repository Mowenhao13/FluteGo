package main

import (
	"FluteGo/pkg/apiserver"
	"FluteGo/pkg/config"
	"FluteGo/pkg/receiver"
	"FluteGo/pkg/system"
	"FluteGo/pkg/utils"
	"FluteGo/pkg/web"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/ncruces/zenity"
	"github.com/schollz/progressbar/v3"
)

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

func defaultDownloadsDir() string {
	home, err := os.UserHomeDir()
	if err == nil {
		d := filepath.Join(home, "Downloads")
		if _, statErr := os.Stat(d); statErr == nil {
			return d
		}
	}
	if runtime.GOOS == "windows" {
		return `C:\Downloads`
	}
	return "."
}

func main() {
	runtime.GOMAXPROCS(8)

	csvFlag := flag.Bool("csv", false, "Save transfer results to CSV file")
	flag.Parse()

	cfg, err := config.Load("config_receiver.json")
	if err != nil {
		log.Printf("[config] load error: %v, using defaults", err)
		cfg = config.Default()
	}

	// Receiver 模式：获取本机实际 IP 地址而不是使用配置中的 127.0.0.1
	destIP := utils.GetLocalIPv4()
	// destIP := "192.168.0.10"
	// destIP := "127.0.0.1"
	log.Printf("[receiver] Using local IP: %s", destIP)

	saveFileDir := cfg.Receiver.SaveFileDir
	if saveFileDir == "" {
		saveFileDir = defaultDownloadsDir()
	}

	if err := os.MkdirAll(saveFileDir, 0755); err != nil {
		log.Printf("Warning: could not create save dir %s: %v", saveFileDir, err)
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting Receiver System...")

	// Enable CSV if --csv flag is set
	if *csvFlag {
		receiver.CsvEnabled = true
		log.Println("[csv] CSV logging enabled")
	}

	// 1. Initialize System.
	sys, err := system.InitReceiverSystem(0, destIP, saveFileDir, true)
	if err != nil {
		log.Fatalf("Failed to initialize system: %v", err)
	}

	// Handle OS signals for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// API server — set up before starting subsystems so OnMetaReceived is wired.
	srv := apiserver.New(cfg.Server.Port, "receiver").
		WithDestIP(destIP).
		WithFilePort(cfg.Network.BaseFilePort)
	sys.OnMetaReceived = func(fdtID uint8, name string, size int64, fec, md5 string) {
		srv.State().RegisterFile("receiver", fdtID, name, size, fec, md5)
	}
	srv.WithStaticContent(web.HTML)
	srv.SetBrowseDirFunc(func() (string, error) {
		return zenity.SelectFile(
			zenity.Title("选择接收目录"),
			zenity.Directory(),
			zenity.Filename(saveFileDir),
		)
	})

	if cfg.Server.Enabled {
		go srv.Start(ctx) //nolint:errcheck
		time.Sleep(300 * time.Millisecond)
		openBrowser(fmt.Sprintf("http://localhost:%d", cfg.Server.Port))
	}

	// 2. Start Error Handling.
	sys.StartErrorProgram()
	log.Println("Error handling subsystem started.")

	// 3. Start Meta Receiver.
	sys.StartMetaProgram()
	log.Println("Meta receiver subsystem started.")

	// 4. Start File Receiver Workers.
	sys.StartFileProgram()
	log.Println("File receiver subsystem started.")

	// Wire API server controls now that the receiver is running.
	srv.SetReceiverReady(true)
	srv.WithSaveDir(saveFileDir)
	srv.SetSaveDirFunc(func(dir string) error {
		return sys.SetSaveDir(dir)
	})

	// 5. Monitor Progress.
	go func() {
		log.Println("Monitoring file progress...")
		completedFiles := 0
		totalFiles := -1

		bars := make(map[uint8]*progressbar.ProgressBar)

		for {
			select {
			case <-ctx.Done():
				return
			case report := <-sys.FileReporter.ReportChan:
				// Update API state for WebSocket clients.
				srv.State().UpdateFromReceiverReport(
					report.FdtID,
					int64(report.ReceivedBytes),
					int64(report.TotalBytes),
					report.Status,
				)
				switch report.Status {
				case 0: // Transferring
					if totalFiles == -1 && report.TotalFiles > 0 {
						totalFiles = int(report.TotalFiles)
						log.Printf("Session total files: %d", totalFiles)
					}

					bar, ok := bars[report.FdtID]
					if !ok {
						bar = progressbar.NewOptions64(
							int64(report.TotalBytes),
							progressbar.OptionSetDescription(fmt.Sprintf("[FdtID:%d]", report.FdtID)),
							progressbar.OptionSetWriter(os.Stderr),
							progressbar.OptionShowBytes(true),
							progressbar.OptionSetWidth(15),
							progressbar.OptionThrottle(65*time.Millisecond),
							progressbar.OptionShowCount(),
							progressbar.OptionOnCompletion(func() {
								fmt.Fprint(os.Stderr, "\n")
							}),
							progressbar.OptionSpinnerType(14),
							progressbar.OptionFullWidth(),
						)
						bars[report.FdtID] = bar
					}
					bar.Set64(int64(report.ReceivedBytes)) //nolint:errcheck

				case 1: // Completed
					completedFiles++
					if bar, ok := bars[report.FdtID]; ok {
						bar.Finish() //nolint:errcheck
						delete(bars, report.FdtID)
					}

					log.Printf("✅ File %d transfer COMPLETED. Total: %d bytes. Progress: %d/%d",
						report.FdtID, report.TotalBytes, completedFiles, totalFiles)

					if totalFiles > 0 && completedFiles >= totalFiles {
						log.Println("All files received. Initiating shutdown...")
						stop()
						return
					}
				case 2: // Error
					if bar, ok := bars[report.FdtID]; ok {
						bar.Finish() //nolint:errcheck
						delete(bars, report.FdtID)
					}
					log.Printf("❌ File %d transfer ERROR.", report.FdtID)
				}
			}
		}
	}()

	// Wait for signal.
	<-ctx.Done()
	log.Println("Shutdown signal received. Cleaning up...")

	time.Sleep(1 * time.Second)
	log.Println("System shutdown complete.")
}
