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

type RsDecoder struct {
	Config        DecoderConfig
	expectedSizes []int64 // 每个chunk的预期大小
	inputs        []*os.File
	receivedBytes []int64
	fileLocks     []sync.Mutex
	decoded       bool // 防止重复解码
}

func NewRsDecoder(config DecoderConfig) (*RsDecoder, error) {
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

	return &RsDecoder{
		Config:        config,
		inputs:        inputs,
		expectedSizes: expectedSizes,
		receivedBytes: make([]int64, totalShards),
		fileLocks:     make([]sync.Mutex, totalShards),
		decoded:       false,
	}, nil
}

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

	enc, err := rs.NewStream(int(r.Config.DataShards), int(r.Config.ParityShards))
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
	return nil
}

func (r *RsDecoder) Close() error {
	return nil
}

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

	log.Printf("Shard %d received: %d/%d bytes (%.1f%%)",
		chunkID, r.receivedBytes[chunkID], r.expectedSizes[chunkID],
		(float64(r.receivedBytes[chunkID])/float64(r.expectedSizes[chunkID]))*100,
	)

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

func (r *RsDecoder) checkComplete() bool {
	allComplete := true
	for i, received := range r.receivedBytes {
		if received < r.expectedSizes[i] {
			allComplete = false
			break
		}
	}

	return allComplete
}

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
