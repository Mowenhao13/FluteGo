package flute_sender

import (
	"Flute_go/pkg/lct"
	"Flute_go/pkg/oti"
	"Flute_go/pkg/sender"
	"Flute_go/pkg/transport"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	Sender SenderConfigSection `yaml:"sender"`
}

type SenderConfigSection struct {
	Network        SenderNetworkConfig `yaml:"network"`
	Fec            SenderFecConfig     `yaml:"fec"`
	Flute          SenderFluteConfig   `yaml:"flute"`
	Logging        SenderLoggingConfig `yaml:"logging"`
	Files          []FileConfig        `yaml:"files"`
	MaxRateKbps    *uint32             `yaml:"max_rate_kbps,omitempty"`        // 额外限速
	SendIntervalUs *uint64             `yaml:"send_interval_micros,omitempty"` // 兼容字段（若你想固定间隔发包）
}

type SenderNetworkConfig struct {
	Destination string `yaml:"destination"`  // "224.0.0.1:3400" / "192.168.0.10:9000"
	BindAddress string `yaml:"bind_address"` // "0.0.0.0"
	BindPort    uint16 `yaml:"bind_port"`    // 0 = 任意
}

type SenderFecConfig struct {
	Type                     string `yaml:"type"` // "no_code" | "reed_solomon_gf28" |
	EncodingSymbolLength     uint16 `yaml:"encoding_symbol_length"`
	MaxNumberOfParitySymbols uint32 `yaml:"max_number_of_parity_symbols"`
	MaximumSourceBlockLength uint32 `yaml:"maximum_source_block_length"`
	SymbolAlignment          uint8  `yaml:"symbol_alignment"`
	SubBlocksLength          uint16 `yaml:"sub_blocks_length"`
}

type SenderFluteConfig struct {
	TSI              uint32 `yaml:"tsi"`
	InterleaveBlocks uint32 `yaml:"interleave_blocks"`
}

type SenderLoggingConfig struct {
	ProgressInterval uint32 `yaml:"progress_interval"`
}

type FileConfig struct {
	Path        string `yaml:"path"`
	ContentType string `yaml:"content_type"`
	Priority    uint8  `yaml:"priority"`
	Version     uint32 `yaml:"version"`
}

func loadConfig(path string) (*AppConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg AppConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	return &cfg, nil
}

// 主程序

func main() {
	configPath := flag.String("config", "config.yaml", "path to YAML config")
	flag.Parse()

	fmt.Printf("[flute-sender] loading config: %s\n", *configPath)
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	var totalFileSize uint64
	for _, f := range cfg.Sender.Files {
		if st, err := os.Stat(f.Path); err == nil {
			totalFileSize += uint64(st.Size())
		}
	}
	fmt.Printf("[flute-sender] total file size: %d bytes (%.2f MB)\n",
		totalFileSize, float64(totalFileSize)/(1024*1024))

	// 构建 UDP endpoint（仅用于 Sender 内部保存 TSI/TSI/目的信息等）
	endpoint := transport.NewUDPEndpoint(
		nil,
		cfg.Sender.Network.BindAddress,
		cfg.Sender.Network.BindPort,
	)

	// 绑定 UDP socket
	bindAddr := fmt.Sprintf("%s:%d", cfg.Sender.Network.BindAddress, cfg.Sender.Network.BindPort)
	fmt.Printf("[flute-sender] bind UDP socket on %s\n", bindAddr)
	udpConn, err := net.ListenPacket("udp", bindAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bind udp failed: %v\n", err)
		os.Exit(1)
	}
	defer udpConn.Close()

	// 解析目的地址
	raddr, err := net.ResolveUDPAddr("udp", cfg.Sender.Network.Destination)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve dest failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[flute-sender] destination: %s\n", raddr.String())

	// 构建 OTI（按配置选择）
	otiConf, err := buildOtiFromConfig(&cfg.Sender.Fec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid FEC/OTI config: %v\n", err)
		os.Exit(1)
	}
	printOti(otiConf)

	// Sender 配置（interleave 等）
	sconf := sender.Config{
		InterleaveBlocks: uint32(cfg.Sender.Flute.InterleaveBlocks),
	}
	// 创建 Sender
	s := sender.NewSender(endpoint, uint64(cfg.Sender.Flute.TSI), otiConf, &sconf)

	// 装载文件
	for _, f := range cfg.Sender.Files {
		if !isFile(f.Path) {
			fmt.Fprintf(os.Stderr, "file not found: %s\n", f.Path)
			continue
		}
		fmt.Printf("[flute-sender] add file: %s\n", f.Path)

		obj, err := senderpkg.CreateObjectDescFromFile(
			filepath.Clean(f.Path),
			nil, // base url
			f.ContentType,
			true, // md5?
			f.Version,
			nil, nil, nil, nil, // groups/optel等（按需）
			lct.CencNull, // 和你工程保持一致（也可从配置扩展）
			true,         // inband_cenc?
			nil,          // target acquisition
			true,         // sender_current_time?
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create object from file failed: %v\n", err)
			continue
		}
		if err := s.AddObject(uint32(f.Priority), obj); err != nil {
			fmt.Fprintf(os.Stderr, "add object failed: %v\n", err)
			continue
		}
	}

	// 发布 FDT
	if err := s.Publish(time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "publish FDT failed: %v\n", err)
		os.Exit(1)
	}

	// 发送循环（带限速 + 进度日志）
	runSendLoop(context.Background(), udpConn, raddr, s, cfg)

}

// 发送循环

func runSendLoop(ctx context.Context, conn net.PacketConn, raddr net.Addr, s *senderpkg.Sender, cfg *AppConfig) {
	start := time.Now()
	var totalBytes uint64
	var pkts uint64

	// 速率控制
	maxRateKbps := uint32(0)
	if cfg.Sender.MaxRateKbps != nil {
		maxRateKbps = *cfg.Sender.MaxRateKbps
	}
	bytesPerSec := 0.0
	if maxRateKbps > 0 {
		bytesPerSec = float64(maxRateKbps) * 1000.0 / 8.0 // kbps → B/s
	}
	fmt.Printf("[flute-sender] rate limit: %d kbps (%d B/s)\n", maxRateKbps, int(bytesPerSec))

	nextSendAt := time.Now()

	// 日志节流
	logEvery := uint64(1000)
	if cfg.Sender.Logging.ProgressInterval > 0 {
		logEvery = uint64(cfg.Sender.Logging.ProgressInterval)
	}
	lastLogAt := time.Now()
	var bytesSinceLog uint64

	for {
		pktb := s.Read(time.Now())
		if pktb == nil {
			break
		}

		// 可选：kbps 限速（逐包节拍）
		if bytesPerSec > 0 {
			interval := time.Duration(float64(len(pktb)) / bytesPerSec * float64(time.Second))
			now := time.Now()
			if now.Before(nextSendAt) {
				time.Sleep(nextSendAt.Sub(now))
			}
			nextSendAt = nextSendAt.Add(interval)

			// 漂移校准（避免累计误差）
			if drift := time.Since(nextSendAt); drift > 200*time.Millisecond {
				nextSendAt = time.Now().Add(interval)
			}
		}

		// 发送
		n, err := conn.WriteTo(pktb, raddr)
		if err != nil {
			// UDP write 出错通常可以继续（网络短暂问题）
			if !errors.Is(err, io.EOF) {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
			continue
		}

		totalBytes += uint64(n)
		bytesSinceLog += uint64(n)
		pkts++

		// 进度日志
		if pkts%logEvery == 0 {
			now := time.Now()
			dt := now.Sub(lastLogAt).Seconds()
			if dt > 0 {
				instMbps := (float64(bytesSinceLog) * 8.0) / dt / 1_000_000.0
				avgMbps := (float64(totalBytes) * 8.0) / now.Sub(start).Seconds() / 1_000_000.0
				fmt.Printf("[flute-sender] progress: %d pkts, %d MB | inst: %.2f Mbps | avg: %.2f Mbps\n",
					pkts, totalBytes/(1024*1024), instMbps, avgMbps)
			}
			lastLogAt = now
			bytesSinceLog = 0
		}
	}

	// 收尾统计
	elapsed := time.Since(start)
	avgMbps := (float64(totalBytes) * 8.0) / elapsed.Seconds() / 1_000_000.0
	fmt.Println("============================================")
	fmt.Println("FILE TRANSFER COMPLETED")
	fmt.Println("============================================")
	fmt.Printf("Total time:      %.2f s\n", elapsed.Seconds())
	fmt.Printf("Total packets:   %d\n", pkts)
	fmt.Printf("Total data sent: %.2f MB\n", float64(totalBytes)/(1024*1024))
	fmt.Printf("Average rate:    %.2f Mbps (%.2f MB/s)\n", avgMbps, avgMbps/8.0)
	fmt.Println("============================================")
}

func buildOtiFromConfig(c *SenderFecConfig) (*oti.Oti, error) {
	switch c.Type {
	case "no_code":
		o := oti.NewNoCode(c.EncodingSymbolLength, c.MaximumSourceBlockLength)
		return &o, nil

	case "reed_solomon_gf28":
		o, err := oti.NewReedSolomonRS28(c.EncodingSymbolLength, c.MaximumSourceBlockLength, uint8(c.MaxNumberOfParitySymbols))
		if err != nil {
			return nil, err
		}
		return &o, nil

	case "reed_solomon_gf28_under_specified":
		o, err := oti.NewReedSolomonRs28UnderSpecified(c.EncodingSymbolLength, c.MaximumSourceBlockLength, uint16(c.MaxNumberOfParitySymbols))
		if err != nil {
			return nil, err
		}
		return &o, nil

	default:
		return nil, fmt.Errorf("unsupported FEC type: %s", c.Type)
	}
}

func printOti(o *oti.Oti) {
	fmt.Printf("[flute-sender] FEC: %v\n", o.FecEncodingID)
	fmt.Printf("[flute-sender] EncodingSymbolLength: %d bytes\n", o.EncodingSymbolLength)
	fmt.Printf("[flute-sender] MaximumSourceBlockLength: %d\n", o.MaximumSourceBlockLength)
	fmt.Printf("[flute-sender] MaxNumberOfParitySymbols: %d\n", o.MaxNumberOfParitySymbols)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
