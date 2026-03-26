package config

import (
	"encoding/json"
	"log"
	"os"
)

// Config holds all runtime-configurable parameters for FluteGo.
type Config struct {
	DestIP   string         `json:"destIP"`
	Network  NetworkConfig  `json:"network"`
	Sender   SenderConfig   `json:"sender"`
	Receiver ReceiverConfig `json:"receiver"`
	Server   ServerConfig   `json:"server"`
}

// NetworkConfig holds UDP transport parameters.
type NetworkConfig struct {
	MaxPacketSize int `json:"maxPacketSize"` // 1408
	BaseFilePort  int `json:"baseFilePort"`  // 3400
	MetaPort      int `json:"metaPort"`      // 3399
	NumPorts      int `json:"numPorts"`      // 1
}

// SenderConfig holds sender-side parameters.
type SenderConfig struct {
	SendFileDir         string  `json:"sendFileDir"`
	SendRedundancyRatio float64 `json:"sendRedundancyRatio"` // 1.05
	RateLimitMbps       int     `json:"rateLimitMbps"`       // 500
	StartSendWait       int     `json:"startSendWait"`       // 1
}

// ReceiverConfig holds receiver-side parameters.
type ReceiverConfig struct {
	SaveFileDir         string  `json:"saveFileDir"`
	RecvRedundancyRatio float64 `json:"recvRedundancyRatio"` // 1.01
	DefaultChunkSize    int     `json:"defaultChunkSize"`    // 65536
}

// ServerConfig holds API server parameters.
type ServerConfig struct {
	Port    int  `json:"port"`    // 8080
	Enabled bool `json:"enabled"` // true
}

// Load reads a JSON file at path and returns a Config.
// If the file does not exist, it logs a warning and returns Default().
// EnsureSaveFileDir adds a trailing slash to SaveFileDir if not present.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[config] %s not found, using defaults", path)
			return Default(), nil
		}
		return nil, err
	}

	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Ensure SaveFileDir ends with a slash
	if cfg.Receiver.SaveFileDir != "" && cfg.Receiver.SaveFileDir[len(cfg.Receiver.SaveFileDir)-1] != '/' {
		cfg.Receiver.SaveFileDir += "/"
	}

	return cfg, nil
}

// Default returns a Config with values matching constant/constant.go.
func Default() *Config {
	return &Config{
		DestIP: "127.0.0.1",
		Network: NetworkConfig{
			MaxPacketSize: 1408,
			BaseFilePort:  3400,
			MetaPort:      3399,
			NumPorts:      1,
		},
		Sender: SenderConfig{
			SendFileDir:         "cmd/send_files/",
			SendRedundancyRatio: 1.05,
			RateLimitMbps:       500,
			StartSendWait:       1,
		},
		Receiver: ReceiverConfig{
			SaveFileDir:         "cmd/received_files/", // 已包含尾部斜杠
			RecvRedundancyRatio: 1.01,
			DefaultChunkSize:    65536,
		},
		Server: ServerConfig{
			Port:    8080,
			Enabled: true,
		},
	}
}
