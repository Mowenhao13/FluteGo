package receiver

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
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
	"symbol_size",
	"chunk_size",
	"max_packet_size",
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
	"total_alloc_mem_bytes",
	"peak_heap_mem_bytes",
	"sys_mem_mb",
	"heap_idle_mem_mb",
	"gc_count",
	"malloc_count",
	"heap_objects",
	"status",
}

type TransferStats struct {
	Timestamp       time.Time
	FdtID           uint8
	FECType         string
	FileName        string
	FileSize        uint64
	SymbolSize      uint16
	ChunkSize       uint32
	MaxPacketSize   uint16
	ChunksFinished  uint32
	ExpectedChunks  uint32
	ChunksRecovered uint32
	ChunksMissing   uint32
	Duration        time.Duration
	BytesReceived   int64
	ThroughputMbps  float64
	PacketsRecv     int64
	ExpectedPkts    int64
	PacketRatio     float64
	MemTotalAlloc   uint64
	MemPeakHeap     uint64
	MemSys          uint64
	MemHeapIdle     uint64
	GCCount         uint32
	MallocCount     uint64
	HeapObjects     uint64
	Status          string
}

func (r *Receiver) CollectStats(status string) TransferStats {
	var memStatsEnd runtime.MemStats
	runtime.ReadMemStats(&memStatsEnd)

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

	return TransferStats{
		Timestamp:       time.Now(),
		FdtID:           r.fdtID,
		FECType:         fecType,
		FileName:        filepath.Base(r.outputPath),
		FileSize:        r.config.FileSize,
		SymbolSize:      r.config.SymbolSize,
		ChunkSize:       r.config.ChunkSize,
		MaxPacketSize:   r.config.MaxPacketSize,
		ChunksFinished:  finished,
		ExpectedChunks:  r.expectedChunks,
		ChunksRecovered: recovered,
		ChunksMissing:   r.expectedChunks - finished,
		Duration:        dur,
		BytesReceived:   bytesWritten,
		ThroughputMbps:  mbps,
		PacketsRecv:     receivedPkts,
		ExpectedPkts:    r.expectedPackets,
		PacketRatio:     pktRatio,
		MemTotalAlloc:   memStatsEnd.TotalAlloc - r.memStatsStart.TotalAlloc,
		MemPeakHeap:     memStatsEnd.HeapAlloc,
		MemSys:          memStatsEnd.Sys,
		MemHeapIdle:     memStatsEnd.HeapIdle,
		GCCount:         memStatsEnd.NumGC - r.memStatsStart.NumGC,
		MallocCount:     memStatsEnd.Mallocs - r.memStatsStart.Mallocs,
		HeapObjects:     memStatsEnd.HeapObjects,
		Status:          status,
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
		fmt.Sprintf("%d", s.ChunkSize),
		fmt.Sprintf("%d", s.MaxPacketSize),
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
		fmt.Sprintf("%d", s.MemTotalAlloc),
		fmt.Sprintf("%d", s.MemPeakHeap),
		fmt.Sprintf("%d", s.MemSys/(1024*1024)),
		fmt.Sprintf("%d", s.MemHeapIdle/(1024*1024)),
		fmt.Sprintf("%d", s.GCCount),
		fmt.Sprintf("%d", s.MallocCount),
		fmt.Sprintf("%d", s.HeapObjects),
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
