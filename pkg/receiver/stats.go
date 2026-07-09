package receiver

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"FluteGo/pkg/decoder"
)

// CsvEnabled 控制是否写入 CSV 统计文件（默认 false，由 --csv 标志启用）
var CsvEnabled = false

// CSVHeader is the header row written once when the CSV file is first created.
var CSVHeader = []string{
	"timestamp",
	"fdt_id",
	"fec_type",
	"file_name",
	"file_size_bytes",
	"symbol_size",           // 固定1024B
	"source_block_size",     // ChunkSize — symbols per source block (RaptorQ核心参数)
	"repair_symbols",        // 修复符号数 = 总发送符号 - source_block_size
	"redundancy_ratio_pct",  // 冗余比例(%) = (repair_symbols / source_block_size) * 100
	"total_sent_symbols",    // 总发送符号数 = packets_received (去掉LCT头部后每个包就是一个符号)
	"chunks_completed",
	"expected_chunks",
	"chunks_recovered",
	"chunks_missing",
	"duration_sec",
	"bytes_received",
	"throughput_mbps",
	"packets_received",
	"expected_packets",
	"packet_ratio_pct",
	"status",
}

type TransferStats struct {
	Timestamp         time.Time
	FdtID             uint8
	FECType           string
	FileName          string
	FileSize          uint64
	SymbolSize        uint16
	SourceBlockSize   uint32   // ChunkSize — symbols per source block (RaptorQ)
	RepairSymbols     uint32   // 修复符号数 = totalSentSymbols - sourceBlockSize (per-chunk近似)
	RedundancyRatioPct float64 // 冗余比例(%)
	TotalSentSymbols  int64    // 总发送符号数 ≈ packets_received
	ChunksFinished    uint32
	ExpectedChunks    uint32
	ChunksRecovered   uint32
	ChunksMissing     uint32
	Duration          time.Duration
	BytesReceived     int64
	ThroughputMbps    float64
	PacketsRecv       int64
	ExpectedPkts      int64
	PacketRatio       float64
	Status            string
}

func (r *Receiver) CollectStats(status string) TransferStats {
	finished := atomic.LoadUint32(&r.finishedChunks)
	bytesWritten := atomic.LoadInt64(&r.currWritten)
	receivedPkts := atomic.LoadInt64(&r.totalPackets)

	var dur time.Duration
	var mbps float64
	if atomic.LoadInt32(&r.receiveStarted) == 1 {
		if !r.receiveEnd.IsZero() {
			dur = r.receiveEnd.Sub(r.receiveStart)
		} else {
			dur = time.Since(r.receiveStart)
		}
		seconds := dur.Seconds()
		if seconds > 0 {
			mbps = (float64(bytesWritten) * 8.0 / seconds) / 1e6
		}
	}

	var fecType string
	switch r.config.Type {
	case 0:
		fecType = "NoCode"
	case 1:
		fecType = "RaptorQ"
	case 2:
		fecType = "ReedSolomon"
	default:
		fecType = "Unknown"
	}

	var pktRatio float64
	if r.expectedPackets > 0 {
		pktRatio = float64(receivedPkts) / float64(r.expectedPackets) * 100
	}

	var recovered uint32
	if rq, ok := r.decoder.(*decoder.RqDecoder); ok {
		recovered = rq.GetRecoveredCount()
	}

	// 计算 RaptorQ 专有参数：
	//   sourceBlockSymbols = ChunkSize (source block 的 symbol 数)
	//   totalSentSymbols ≈ receivedPkts
	//   repairSymbols ≈ totalSentSymbols - sourceBlockSymbols (近似值，最后一块可能小于 ChunkSize，
	//     但对于 CSV 统计已足够体现冗余比例)
	sourceBlockSize := r.config.ChunkSize
	repair := receivedPkts - int64(sourceBlockSize)
	if repair < 0 {
		repair = 0
	}
	var redundancyPct float64
	if sourceBlockSize > 0 {
		redundancyPct = float64(repair) / float64(sourceBlockSize) * 100
	}

	return TransferStats{
		Timestamp:          time.Now(),
		FdtID:              r.fdtID,
		FECType:            fecType,
		FileName:           filepath.Base(r.outputPath),
		FileSize:           r.config.FileSize,
		SymbolSize:         r.config.SymbolSize,
		SourceBlockSize:    sourceBlockSize,
		RepairSymbols:      uint32(repair),
		RedundancyRatioPct: redundancyPct,
		TotalSentSymbols:   receivedPkts,
		ChunksFinished:     finished,
		ExpectedChunks:     r.expectedChunks,
		ChunksRecovered:    recovered,
		ChunksMissing:      r.expectedChunks - finished,
		Duration:           dur,
		BytesReceived:      bytesWritten,
		ThroughputMbps:     mbps,
		PacketsRecv:        receivedPkts,
		ExpectedPkts:       r.expectedPackets,
		PacketRatio:        pktRatio,
		Status:             status,
	}
}

func (s TransferStats) toCSVRow() []string {
	return []string{
		s.Timestamp.Format("2006-01-02 15:04:05.000000"),
		fmt.Sprintf("%d", s.FdtID),
		s.FECType,
		s.FileName,
		fmt.Sprintf("%d", s.FileSize),
		fmt.Sprintf("%d", s.SymbolSize),
		fmt.Sprintf("%d", s.SourceBlockSize),
		fmt.Sprintf("%d", s.RepairSymbols),
		fmt.Sprintf("%.2f", s.RedundancyRatioPct),
		fmt.Sprintf("%d", s.TotalSentSymbols),
		fmt.Sprintf("%d", s.ChunksFinished),
		fmt.Sprintf("%d", s.ExpectedChunks),
		fmt.Sprintf("%d", s.ChunksRecovered),
		fmt.Sprintf("%d", s.ChunksMissing),
		fmt.Sprintf("%.6f", s.Duration.Seconds()),
		fmt.Sprintf("%d", s.BytesReceived),
		fmt.Sprintf("%.4f", s.ThroughputMbps),
		fmt.Sprintf("%d", s.PacketsRecv),
		fmt.Sprintf("%d", s.ExpectedPkts),
		fmt.Sprintf("%.2f", s.PacketRatio),
		s.Status,
	}
}

func WriteTransferCSV(saveDir string, stats TransferStats) {
	if !CsvEnabled {
		return
	}
	if saveDir == "" {
		saveDir = "."
	}
	csvPath := filepath.Join(saveDir, "transfer_stats.csv")

	writeHeader := false
	if _, err := os.Stat(csvPath); os.IsNotExist(err) {
		writeHeader = true
	}

	f, err := os.OpenFile(csvPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[stats] Failed to open CSV file %s: %v", csvPath, err)
		return
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if writeHeader {
		if err := w.Write(CSVHeader); err != nil {
			log.Printf("[stats] Failed to write CSV header: %v", err)
			return
		}
	}

	if err := w.Write(stats.toCSVRow()); err != nil {
		log.Printf("[stats] Failed to write CSV row: %v", err)
		return
	}

	log.Printf("[stats] Transfer stats written to %s", csvPath)
}
