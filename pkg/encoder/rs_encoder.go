package encoder

import (
	constant "FluteGo/constant"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/edsrzf/mmap-go"
	rs "github.com/klauspost/reedsolomon"
	"golang.org/x/sys/unix"
)

type RsEncoder struct {
	Config EncoderConfig
	RsExtraParam
	Callback SendCallback
}

type RsExtraParam struct {
	// SIMD指令集优化（性能从低到高）
	WithSSE2    bool // SSE2指令集（x86基础）
	WithSSSE3   bool // SSSE3指令集
	WithAVX2    bool // AVX2指令集（推荐，现代CPU）
	WithAVX512  bool // AVX512指令集（服务器级CPU）
	WithAVXGFNI bool // AVX+GFNI指令集（最新Intel）
	WithGFNI    bool // GFNI指令集（Galois Field新指令）

	// 并发控制
	WithConcurrentStreamReads  bool // 并发读取流
	WithConcurrentStreamWrites bool // 并发写入流
	WithConcurrentStreams      bool // 同时启用读写并发

	// 高级编码技术
	WithInversionCache bool // 逆矩阵缓存（多次解码优化）
}

func loadExtraParams() RsExtraParam {
	return RsExtraParam{
		WithSSE2:    constant.RsWithSSE2,
		WithSSSE3:   constant.RsWithSSSE3,
		WithAVX2:    constant.RsWithAVX2,
		WithAVX512:  constant.RsWithAVX512,
		WithAVXGFNI: constant.RsWithAVXGFNI,
		WithGFNI:    constant.RsWithGFNI,

		WithConcurrentStreamReads:  constant.RsWithConcurrentStreamReads,
		WithConcurrentStreamWrites: constant.RsWithConcurrentStreamWrites,
		WithConcurrentStreams:      constant.RsWithConcurrentStreams,

		WithInversionCache: constant.RsWithInversionCache,
	}
}

func NewRsEncoder(config EncoderConfig) (*RsEncoder, error) {
	rsExtraParam := loadExtraParams()
	return &RsEncoder{
		Config:       config,
		RsExtraParam: rsExtraParam,
	}, nil
}

func (e *RsEncoder) encode() error {
	dataShards := int(e.Config.DataShards)
	parityShards := int(e.Config.ParityShards)
	totalShards := dataShards + parityShards

	enc, err := rs.NewStream(dataShards, parityShards,
		rs.WithSSE2(e.RsExtraParam.WithSSE2),
		rs.WithSSSE3(e.RsExtraParam.WithSSSE3),
		rs.WithAVX2(e.RsExtraParam.WithAVX2),
		rs.WithAVX512(e.RsExtraParam.WithAVX512),
		rs.WithAVXGFNI(e.RsExtraParam.WithAVXGFNI),
		rs.WithGFNI(e.RsExtraParam.WithGFNI),
		rs.WithConcurrentStreamReads(e.RsExtraParam.WithConcurrentStreamReads),
		rs.WithConcurrentStreamWrites(e.RsExtraParam.WithConcurrentStreamWrites),
		rs.WithConcurrentStreams(e.RsExtraParam.WithConcurrentStreams),
		rs.WithInversionCache(e.RsExtraParam.WithInversionCache),
	)
	if err != nil {
		return fmt.Errorf("failed to create Reed-Solomon encoder: %w", err)
	}

	out := make([]*os.File, totalShards)
	dir, file := filepath.Split(e.Config.FName)
	if constant.RsTmpSendOutDir != "" {
		dir = constant.RsTmpSendOutDir
	}

	log.Printf("RS encoder will write shards to dir: %s (base filename: %s)", dir, file)

	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Printf("Failed to create RS temp dir %s: %v", dir, err)
			return err
		}
	}

	for i := range out {
		outfn := fmt.Sprintf("%s.%d", file, i)
		fullPath := filepath.Join(dir, outfn)
		out[i], err = os.Create(fullPath)
		if err != nil {
			log.Printf("Failed to create shard file %s: %v", fullPath, err)
			return err
		}
		log.Printf("Created shard file: %s", fullPath)
	}

	data := make([]io.Writer, dataShards)
	for i := range data {
		data[i] = out[i]
	}

	// Open source file and split into data shards first
	srcFile, err := os.Open(e.Config.FName)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", e.Config.FName, err)
	}
	defer srcFile.Close()

	log.Println("Splitting source into data shards...")
	if err := enc.Split(srcFile, data, int64(e.Config.FileSize)); err != nil {
		return fmt.Errorf("split failed: %w", err)
	}
	log.Println("Encoding parity...")
	input := make([]io.Reader, dataShards)

	for i := range data {
		if err := out[i].Close(); err != nil {
			return err
		}
		f, err := os.Open(out[i].Name())
		if err != nil {
			return err
		}
		defer f.Close()
		input[i] = f
	}

	// Validate input shards (ensure non-empty and readable)
	for i, r := range input {
		if r == nil {
			return fmt.Errorf("data shard %d reader is nil", i)
		}
		if f, ok := r.(*os.File); ok {
			st, err := f.Stat()
			if err != nil {
				return fmt.Errorf("failed to stat data shard %d: %w", i, err)
			}
			log.Printf("Data shard %d size: %d bytes", i, st.Size())
			if st.Size() == 0 {
				return fmt.Errorf("ENCODE_FAILED: data shard %d is empty", i)
			}
			// ensure read offset at start
			f.Seek(0, 0)
		}
	}

	parity := make([]io.Writer, parityShards)
	for i := range parity {
		parity[i] = out[dataShards+i]
		defer out[dataShards+i].Close()
	}

	err = enc.Encode(input, parity)
	if err != nil {
		return fmt.Errorf("ENCODE_FAILED: %w", err)
	}

	fmt.Printf("File successfully split into %d data + %d parity shards.\n", dataShards, parityShards)

	return nil
}

// extractShardIndex extracts the numeric suffix from a shard filename
// e.g., "file.bin.3" -> 3, "file.bin" -> 0
func extractShardIndex(filename string) int {
	parts := strings.Split(filename, ".")
	if len(parts) > 0 {
		if idx, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return idx
		}
	}
	return 0
}

func (e *RsEncoder) Encode(ctx context.Context, chunkCount uint32, provider DataProvider, cb SendCallback) error {
	callback := cb
	if callback == nil {
		callback = e.Callback
	}

	if err := e.encode(); err != nil {
		return fmt.Errorf("failed to encode data: %w", err)
	}

	fs, err := os.ReadDir(constant.RsTmpSendOutDir)
	if err != nil {
		return fmt.Errorf("failed to read temp dir: %w", err)
	}

	// Sort files by numeric suffix (e.g., file.0, file.1, file.10, file.2 -> sorted as 0, 1, 2, 10)
	sort.Slice(fs, func(i, j int) bool {
		iIdx := extractShardIndex(fs[i].Name())
		jIdx := extractShardIndex(fs[j].Name())
		return iIdx < jIdx
	})

	for _, fn := range fs {
		if !fn.Type().IsRegular() {
			continue
		}
		if !strings.HasPrefix(fn.Name(), filepath.Base(e.Config.FName)+".") {
			continue
		}

		log.Printf("Found shard: %s\n", fn.Name())

		// debug: report parsed shard index
		partsDbg := strings.Split(fn.Name(), ".")
		if len(partsDbg) > 0 {
			if ix, err := strconv.Atoi(partsDbg[len(partsDbg)-1]); err == nil {
				log.Printf("Parsed shard index %d from filename %s", ix, fn.Name())
			}
		}

		f, err := os.Open(filepath.Join(constant.RsTmpSendOutDir, fn.Name()))
		if err != nil {
			return fmt.Errorf("failed to open shard %s: %w", fn.Name(), err)
		}
		defer f.Close()

		instat, err := f.Stat()
		if err != nil {
			return fmt.Errorf("failed to stat shard %s: %w", fn.Name(), err)
		}
		log.Printf("Shard %s size: %d bytes\n", fn.Name(), instat.Size())

		sz := int(instat.Size())

		var shardData []byte 
		offset := 0
		if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
			shardData, err = unix.Mmap(int(f.Fd()), int64(offset), sz, unix.PROT_READ, unix.MAP_SHARED)
			if err != nil {
				return fmt.Errorf("mmap failed for shard %s: %w", fn.Name(), err)
			}
		} 
		if runtime.GOOS == "windows" {
			shardDat, err := mmap.MapRegion(f, sz, mmap.RDONLY, 0, int64(offset))
			if err != nil {
				return fmt.Errorf("mmap failed for shard %s: %w", fn.Name(), err)
			}
			shardData = []byte(shardDat)
		}

		if len(shardData) != sz {
			return fmt.Errorf("mmap size mismatch for shard %s: expected %d, got %d", fn.Name(), sz, len(shardData))
		}

		// determine shard index from filename suffix
		shardIdx := 0
		parts := strings.Split(fn.Name(), ".")
		if len(parts) > 0 {
			if ix, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
				shardIdx = ix
			}
		}

		for i := 0; i < sz; i += int(e.Config.SymbolSize) {
			start := i
			end := i + int(e.Config.SymbolSize)
			if end > sz {
				end = sz
			}

			symbolIdx := i / int(e.Config.SymbolSize)
			symData := shardData[start:end]
			// 添加调试日志
			log.Printf("DEBUG: Calling callback with shardIdx=%d, symbolIdx=%d, shardSize=%d, dataLen=%d",
				shardIdx, symbolIdx, sz, len(symData))
			if err := callback(uint32(shardIdx), uint32(symbolIdx), uint32(sz), symData); err != nil {
				return fmt.Errorf("callback failed for shard %d symbol %d: %w", shardIdx, symbolIdx, err)
			}
		}

		if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
			unix.Munmap(shardData)
		}
		if runtime.GOOS == "windows" {
			shardData := mmap.MMap(shardData)
			shardData.Unmap()
		}
	}

	return nil
}

func (e *RsEncoder) SetCallback(cb SendCallback) {
	e.Callback = cb
}

// Close 释放编码器资源
func (e *RsEncoder) Close() error {
	// No specific resources to release for Reed-Solomon encoder
	return nil
}
