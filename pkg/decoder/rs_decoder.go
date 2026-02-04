/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package decoder

import (
	"FluteGo/constant"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	rs "github.com/klauspost/reedsolomon"
)

// RsDecoder 实现基于 Reed-Solomon 的 chunk 恢复逻辑。
//
// # 工作方式
//
// 在磁盘上构建每个 shard 的临时文件，随着符号写入逐渐填充，最终调用 `decode` 与 `output` 回调完成 chunk 写回。
type RsDecoder struct {
	Config DecoderConfig
	RsExtraParam
	expectedSizes []int64 // 每个chunk的预期大小
	inputs        []*os.File
	receivedBytes []int64
	fileLocks     []sync.Mutex
	decoded       bool // 防止重复解码
	output        OutputHandler
}

// RsExtraParam 存放可选的 SIMD 与并发策略开关。
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

// loadExtraParams 从常量中读取当前编译平台支持的优化特性。
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

// NewRsDecoder creates a Reed-Solomon decoder. It accepts an OutputHandler
// which will be invoked for each decoded chunk once reconstruction/join is done.
// NewRsDecoder 初始化 Reed-Solomon 解码器，预分配各 shard 的临时文件。
//
// # 参数
//
//   - `config`: `DecoderConfig`
//   - `output`: `OutputHandler`
//
// # 返回值
//
//	`RsDecoder` 实例以及初始化过程中的错误。
func NewRsDecoder(config DecoderConfig, output OutputHandler) (*RsDecoder, error) {
	totalShards := int(config.DataShards + config.ParityShards)
	recvDir := filepath.Dir(constant.RsTmpRecvInDir)
	if err := os.MkdirAll(recvDir, 0755); err != nil {
		return nil, err
	}

	// Pre-allocate shard files even if they don't exist yet
	// This allows AddSymbol to write to them as data arrives
	inputs := make([]*os.File, totalShards)
	expectedSizes := make([]int64, totalShards)

	for i := range inputs {
		infn := fmt.Sprintf("%s.%d", config.FName, i)

		// Try to open existing file first
		f, err := os.OpenFile(infn, os.O_RDWR, 0644)
		if err != nil {
			// File doesn't exist, create it with size from config
			// For RS decode, each shard should be ~fileSize/dataShards
			expectedSize := int64(config.FileSize) / int64(config.DataShards)
			if config.FileSize%uint64(config.DataShards) > 0 {
				expectedSize++ // round up for last shard
			}

			f, err = os.Create(infn)
			if err != nil {
				return nil, fmt.Errorf("create shard file %s: %w", infn, err)
			}

			// Pre-allocate space by truncating to expected size
			if err := f.Truncate(expectedSize); err != nil {
				f.Close()
				return nil, fmt.Errorf("truncate shard file %s: %w", infn, err)
			}

			expectedSizes[i] = expectedSize
			log.Printf("Created and pre-allocated shard file: %s (%d bytes)", infn, expectedSize)
		} else {
			// File exists, get its current size
			stat, err := f.Stat()
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("stat shard file %s: %w", infn, err)
			}
			expectedSizes[i] = stat.Size()
			log.Printf("Using existing shard file: %s (%d bytes)", infn, stat.Size())
		}

		inputs[i] = f
	}

	rsExtraParam := loadExtraParams()
	d := &RsDecoder{
		Config:        config,
		RsExtraParam:  rsExtraParam,
		inputs:        inputs,
		expectedSizes: expectedSizes,
		receivedBytes: make([]int64, totalShards),
		fileLocks:     make([]sync.Mutex, totalShards),
		decoded:       false,
		output:        output,
	}
	// Attach output handler by storing it in Config.FName or using closure? We'll
	// perform callbacks in decode() where we have access to output.
	_ = d
	return d, nil
}

// openInput 打开各个 shard 文件并返回其 Reader 列表。
//
// # 返回值
//
//	`[]io.Reader`: 每个 shard 的 reader
//	`int64`: 当前最大 shard 大小
//	`error`: 打开过程中的错误
func (r *RsDecoder) openInput() ([]io.Reader, int64, error) {
	totalShards := int(r.Config.DataShards + r.Config.ParityShards)
	shards := make([]io.Reader, totalShards)
	var maxSize int64

	for i := range shards {
		infn := fmt.Sprintf("%s.%d", r.Config.FName, i)
		f, err := os.Open(infn)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("  Missing shard: %s\n", infn)
				shards[i] = nil
				continue
			}
			return nil, 0, fmt.Errorf("failed to open shard file %s: %w", infn, err)
		}

		stat, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, fmt.Errorf("failed to stat shard file %s: %w", infn, err)
		}

		if stat.Size() > 0 {
			shards[i] = f
			if stat.Size() > maxSize {
				maxSize = stat.Size()
			}
			fmt.Printf("  ✓ %s (%d bytes)\n", infn, stat.Size())
		} else {
			fmt.Printf("  ✗ Empty shard: %s\n", infn)
			shards[i] = nil
			f.Close()
		}
	}

	return shards, maxSize, nil
}

// decode 执行 Reed-Solomon 还原流程，并在成功后触发 `output` 回调。
func (r *RsDecoder) decode() error {
	outDir := filepath.Dir(r.Config.FName)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// Sync all input files to disk before decoding
	for i, f := range r.inputs {
		if f != nil {
			if err := f.Sync(); err != nil {
				log.Printf("Warning: failed to sync shard %d: %v", i, err)
			}
		}
	}

	dataShards := int(r.Config.DataShards)
	parityShards := int(r.Config.ParityShards)

	enc, err := rs.NewStream(dataShards, parityShards,
		rs.WithSSE2(r.RsExtraParam.WithSSE2),
		rs.WithSSSE3(r.RsExtraParam.WithSSSE3),
		rs.WithAVX2(r.RsExtraParam.WithAVX2),
		rs.WithAVX512(r.RsExtraParam.WithAVX512),
		rs.WithAVXGFNI(r.RsExtraParam.WithAVXGFNI),
		rs.WithGFNI(r.RsExtraParam.WithGFNI),
		rs.WithConcurrentStreamReads(r.RsExtraParam.WithConcurrentStreamReads),
		rs.WithConcurrentStreamWrites(r.RsExtraParam.WithConcurrentStreamWrites),
		rs.WithConcurrentStreams(r.RsExtraParam.WithConcurrentStreams),
		rs.WithInversionCache(r.RsExtraParam.WithInversionCache),
	)
	if err != nil {
		return fmt.Errorf("create encoder: %w", err)
	}

	log.Println("Opening input shards...")
	shards, sz, err := r.openInput()
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}

	defer func() {
		for _, s := range shards {
			if c, ok := s.(io.Closer); ok && s != nil {
				c.Close()
			}
		}
	}()

	// Before verification, ensure all critical shards have sufficient data
	validShards := 0
	for i, s := range shards {
		if s != nil {
			validShards++
			log.Printf("Shard %d is available for verification (received: %d bytes)", i, r.receivedBytes[i])
		} else {
			log.Printf("Shard %d is missing/empty", i)
		}
	}
	log.Printf("Total valid shards: %d/%d (need at least %d)", validShards, len(shards), r.Config.DataShards)

	log.Println("Verifying shards...")
	ok, err := enc.Verify(shards)
	if err != nil {
		log.Printf("Verify error: %v", err)
		return fmt.Errorf("verify: %w", err)
	}

	// enc.Verify may have read from the shard readers; reset their offsets to the start
	for _, s := range shards {
		if s == nil {
			continue
		}
		if seeker, ok := s.(io.Seeker); ok {
			seeker.Seek(0, 0)
		}
	}

	totalSize := int64(r.Config.DataShards) * sz
	log.Printf("Total data size: %d bytes (%.2f MB)\n", totalSize, float64(totalSize)/1024/1024)

	if !ok {
		log.Println("ℹ️ Verification check indicated missing/damaged shards. Attempting reconstruction...")
		// 重新打开文件用于重建
		for _, s := range shards {
			if c, ok := s.(io.Closer); ok && s != nil {
				c.Close()
			}
		}

		shards, sz, err = r.openInput()
		if err != nil {
			return fmt.Errorf("reopen for reconstruction: %w", err)
		}

		// 创建输出文件用于重建
		outFiles := make([]*os.File, len(shards))
		defer func() {
			for _, f := range outFiles {
				if f != nil {
					f.Close()
				}
			}
		}()

		outputWriters := make([]io.Writer, len(shards))
		needReconstruction := false

		for i := range shards {
			if shards[i] == nil {
				needReconstruction = true
				outfn := fmt.Sprintf("%s.%d", r.Config.FName, i)
				f, err := os.Create(outfn)
				if err != nil {
					return fmt.Errorf("create output shard %s: %w", outfn, err)
				}
				outFiles[i] = f
				outputWriters[i] = f
			}
		}

		if needReconstruction {
			log.Println("🔧 Reconstructing missing/damaged shards...")
			err = enc.Reconstruct(shards, outputWriters)
			if err != nil {
				return fmt.Errorf("reconstruct failed: %w", err)
			}

			// 关闭并重新打开
			for i := range shards {
				if c, ok := shards[i].(io.Closer); ok && shards[i] != nil {
					c.Close()
				}
			}
			for _, f := range outFiles {
				if f != nil {
					f.Close()
				}
			}

			shards, sz, err = r.openInput()
			if err != nil {
				return fmt.Errorf("reopen after reconstruction: %w", err)
			}

			// Reset all seekers to start before verification
			for _, s := range shards {
				if s == nil {
					continue
				}
				if seeker, ok := s.(io.Seeker); ok {
					seeker.Seek(0, 0)
				}
			}

			ok, err = enc.Verify(shards)
			if !ok {
				return fmt.Errorf("verification failed after reconstruction")
			}
			if err != nil {
				return fmt.Errorf("verify after reconstruction: %w", err)
			}

			// Reset seekers again for Join operation
			for _, s := range shards {
				if s == nil {
					continue
				}
				if seeker, ok := s.(io.Seeker); ok {
					seeker.Seek(0, 0)
				}
			}

			log.Println("✓ Reconstruction successful")
		}
	} else {
		log.Println("✓ All shards verified successfully (no reconstruction needed)")
	}

	// Ensure all seekers are at start before Join
	for _, s := range shards {
		if s == nil {
			continue
		}
		if seeker, ok := s.(io.Seeker); ok {
			seeker.Seek(0, 0)
		}
	}

	// Determine output file path
	// If OutputPath is specified, use it; otherwise use FName (temp directory)
	outputPath := r.Config.FName
	if r.Config.OutputPath != "" {
		outputPath = r.Config.OutputPath
	}

	// Ensure output directory exists
	outDir = filepath.Dir(outputPath)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer f.Close()

	outSize := totalSize
	if r.Config.FileSize > 0 && int(r.Config.FileSize) < int(totalSize) {
		outSize = int64(r.Config.FileSize)
	}
	log.Printf("Joining shards with outSize=%d to %s\n", outSize, outputPath)

	err = enc.Join(f, shards, outSize)
	if err != nil {
		return fmt.Errorf("join failed: %w", err)
	}

	log.Printf("✓ File successfully decoded and written to: %s", outputPath)

	// If output handler provided, invoke per-chunk callbacks
	if r.output != nil {
		// Open the output file and stream per-chunk to callback
		of, err := os.Open(outputPath)
		if err != nil {
			return fmt.Errorf("open output for callbacks: %w", err)
		}
		defer of.Close()

		chunkSize := int64(r.Config.ChunkSize)
		if chunkSize <= 0 {
			chunkSize = int64(constant.DefaultChunkSize)
		}
		totalChunks := int64((outSize + chunkSize - 1) / chunkSize)

		for i := int64(0); i < totalChunks; i++ {
			offset := i * chunkSize
			readLen := chunkSize
			if offset+readLen > outSize {
				readLen = outSize - offset
			}
			buf := make([]byte, readLen)
			if _, err := of.ReadAt(buf, offset); err != nil && err != io.EOF {
				return fmt.Errorf("read output chunk %d: %w", i, err)
			}
			// Invoke callback (chunkIdx is uint32)
			if err := r.output.OnDecodedData(buf, offset, uint32(i)); err != nil {
				log.Printf("warning: output handler OnDecodedData returned error for chunk %d: %v", i, err)
			}
		}
	}

	return nil
}

// Close 释放 RsDecoder 保留的资源并清理临时文件。
func (r *RsDecoder) Close() error {
	var firstErr error

	// 1. 关闭所有打开的文件句柄
	for i, f := range r.inputs {
		if f != nil {
			if err := f.Close(); err != nil {
				log.Printf("Warning: failed to close shard file %d: %v", i, err)
				if firstErr == nil {
					firstErr = err
				}
			}
			r.inputs[i] = nil
		}
	}

	// 2. 删除所有临时分片文件
	totalShards := int(r.Config.DataShards + r.Config.ParityShards)
	for i := 0; i < totalShards; i++ {
		infn := fmt.Sprintf("%s.%d", r.Config.FName, i)
		if err := os.Remove(infn); err != nil && !os.IsNotExist(err) {
			log.Printf("Warning: failed to remove temp file %s: %v", infn, err)
		} else {
			// log.Printf("Cleaned up temp file: %s", infn)
		}
	}

	return firstErr
}

// AddSymbol 将 symbol 写入对应 shard 并在满足条件时触发解码。
func (r *RsDecoder) AddSymbol(chunkID uint32, symbolID uint32, data []byte) error {
	// Skip if already decoded
	if r.decoded {
		return nil
	}

	offset := int64(symbolID) * int64(r.Config.SymbolSize)

	// Bounds check: warn if write would exceed expected shard size
	if r.expectedSizes[chunkID] > 0 && offset+int64(len(data)) > r.expectedSizes[chunkID] {
		log.Printf("Warning: shard %d symbol %d exceeds expected size (offset=%d, len=%d, expected=%d)",
			chunkID, symbolID, offset, len(data), r.expectedSizes[chunkID])
	}

	r.fileLocks[chunkID].Lock()
	defer r.fileLocks[chunkID].Unlock()

	nWritten, err := r.inputs[chunkID].WriteAt(data, offset)
	if err != nil {
		return fmt.Errorf("write shard %d symbol %d (offset=%d): %w", chunkID, symbolID, offset, err)
	}
	r.receivedBytes[chunkID] += int64(nWritten)

	// log.Printf("Shard %d received: %d/%d bytes (%.1f%%)",
	// 	chunkID, r.receivedBytes[chunkID], r.expectedSizes[chunkID],
	// 	(float64(r.receivedBytes[chunkID])/float64(r.expectedSizes[chunkID]))*100,
	// )

	// Check if we have enough shards to decode
	if r.canDecode() {
		log.Println("Sufficient shards received. Starting decoding...")
		r.decoded = true // Mark as decoded to prevent further decoding
		if err := r.decode(); err != nil {
			return fmt.Errorf("decode failed: %w", err)
		}
	}
	return nil
}

// canDecode 判断当前收集的 shard 是否足以触发 Reed-Solomon 解码。
func (r *RsDecoder) canDecode() bool {
	// Only decode once we have all data shards completely received
	// OR when we have at least dataShards complete shards (including parity)
	completedShards := 0
	for i, received := range r.receivedBytes {
		if received == r.expectedSizes[i] && received > 0 {
			completedShards++
		}
	}

	// We need at least DataShards complete shards to decode
	return completedShards >= int(r.Config.DataShards)
}
