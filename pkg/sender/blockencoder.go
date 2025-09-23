package sender

import (
	"Flute_go/pkg/object"
	"context"
	"errors"
	"io"
	"log"
	"sync"
)

var EnableBlockParallel = false
var MaxParallelBuild = 4

type BlockEncoder struct {
	file *FileDesc

	currContentOffset uint64
	currSBN           uint32

	aLarge   uint64
	aSmall   uint64
	nbALarge uint64
	nbBlocks uint64
	blocks   []*Block // 活动窗口中的块
	winSize  int      // block_multiplex_windows
	winIndex int
	readEnd  bool

	sourceSizeTransferred int
	nbPktSent             int

	stopped        bool
	closableObject bool

	// 内部互斥：若 Read() 仅被单协程调用，可不必使用；保守起见加上
	mu sync.Mutex
}

func NewBlockEncoder(file *FileDesc, blockMultiplexWindows int, closableObject bool) (*BlockEncoder, error) {
	// 对齐 Rust：当数据源为 Stream 时 seek 到起点
	switch file.Object.Source.Choice {
	case DataBuffer:
		// no-op
	case DataStream:
		file.Object.Source.streamMu.Lock()
		if seeker, ok := file.Object.Source.stream.(interface {
			Seek(offset int64, whence int) (int64, error)
		}); ok {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				file.Object.Source.streamMu.Unlock()
				return nil, errors.New("seek stream failed: " + err.Error())
			}
		}
		file.Object.Source.streamMu.Unlock()
	default:
		return nil, errors.New("unknown data source")
	}

	be := &BlockEncoder{
		file:                  file,
		currContentOffset:     0,
		currSBN:               0,
		aLarge:                0,
		aSmall:                0,
		nbALarge:              0,
		nbBlocks:              0,
		blocks:                make([]*Block, 0, blockMultiplexWindows),
		winSize:               blockMultiplexWindows,
		winIndex:              0,
		readEnd:               false,
		sourceSizeTransferred: 0,
		nbPktSent:             0,
		stopped:               false,
		closableObject:        closableObject,
	}

	be.blockPartitioning()
	return be, nil
}

func (b *BlockEncoder) blockPartitioning() {
	oti := &b.file.Oti
	b.aLarge, b.aSmall, b.nbALarge, b.nbBlocks = object.BlockPartitioning(
		uint64(oti.MaximumSourceBlockLength),
		b.file.Object.TransferLength,
		uint64(oti.EncodingSymbolLength),
	)
}

func (b *BlockEncoder) Read(forceCloseObject bool) (*object.Pkt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopped {
		return nil, nil
	}
	if forceCloseObject {
		b.stopped = true
	}

	for {
		if err := b.readWindow(context.Background()); err != nil {
			// handle error —— 这里记录并让窗口自然耗尽
			log.Printf("block encoder: readWindow failed: %v", err)
			b.readEnd = true
		}

		if len(b.blocks) == 0 {
			if b.nbPktSent == 0 {
				// 空文件：发送带 close_object 的空包
				log.Printf("Empty file ? Send a pkt containing close-object flag")
				b.nbPktSent++
				if b.file.Object.TransferLength != 0 {
					log.Printf("warn: transfer_length != 0 while sending empty close pkt")
				}
				return &object.Pkt{
					Payload:           nil,
					TransferLength:    b.file.Object.TransferLength,
					Esi:               0,
					Sbn:               0,
					Toi:               b.file.TOI,
					FdtID:             b.file.FdtID,
					Cenc:              b.file.Object.Cenc,
					InbandCenc:        b.file.Object.InbandCenc,
					CloseObject:       true,
					SourceBlockLength: 0,
					SenderCurrentTime: b.file.SenderCurrentTime,
				}, nil
			}
			// 窗口已空：结束
			return nil, nil
		}

		if b.winIndex >= len(b.blocks) {
			b.winIndex = 0
		}

		blk := b.blocks[b.winIndex]
		sym, isLastSymbol := blk.Read()
		if sym == nil {
			// 该块已空，从窗口移除；保持 winIndex 指向当前位置
			b.blocks = append(b.blocks[:b.winIndex], b.blocks[b.winIndex+1:]...)
			continue
		}

		b.winIndex++

		if sym.IsSourceSymbol {
			b.sourceSizeTransferred += len(sym.Symbols)
		}
		b.nbPktSent++

		isLastPacket := (b.sourceSizeTransferred >= int(b.file.Object.TransferLength)) && isLastSymbol

		return &object.Pkt{
			Payload:           append([]byte(nil), sym.Symbols...), // 等价 Rust to_vec()
			TransferLength:    b.file.Object.TransferLength,
			Esi:               sym.Esi,
			Sbn:               sym.Sbn,
			Toi:               b.file.TOI,
			FdtID:             b.file.FdtID,
			Cenc:              b.file.Object.Cenc,
			InbandCenc:        b.file.Object.InbandCenc,
			CloseObject:       forceCloseObject || (b.closableObject && isLastPacket),
			SourceBlockLength: uint32(blk.NbSourceSymbols),
			SenderCurrentTime: b.file.SenderCurrentTime,
		}, nil
	}
}

// ----------------- 读取/构建块 -----------------

func (b *BlockEncoder) readWindow(ctx context.Context) error {
	for !b.readEnd && len(b.blocks) < b.winSize {
		if EnableBlockParallel {
			if err := b.readBlockParallelOnce(ctx); err != nil {
				return err
			}
		} else {
			if err := b.readBlockOnce(); err != nil {
				b.readEnd = true
				return err
			}
		}
	}
	return nil
}

func (b *BlockEncoder) readBlockOnce() error {
	switch b.file.Object.Source.Choice {
	case DataBuffer:
		return b.readBlockBuffer()
	case DataStream:
		return b.readBlockStream()
	default:
		return errors.New("unknown data source")
	}
}

func (b *BlockEncoder) readBlockParallelOnce(ctx context.Context) error {
	// 这里保持一回一块（不改变结构）。需要更猛并发可在此循环拉 K 块并发构建。
	return b.readBlockOnce()
}

func (b *BlockEncoder) readBlockBuffer() error {
	content := b.file.Object.Source.buffer

	oti := &b.file.Oti
	var blockLen uint64
	if uint64(b.currSBN) < b.nbALarge {
		blockLen = b.aLarge
	} else {
		blockLen = b.aSmall
	}

	offsetStart := int(b.currContentOffset)
	offsetEnd := offsetStart + int(blockLen*uint64(oti.EncodingSymbolLength))
	if offsetEnd > len(content) {
		offsetEnd = len(content)
	}
	if offsetStart < 0 || offsetStart > len(content) {
		return errors.New("buffer offset out of range")
	}

	slice := content[offsetStart:offsetEnd]
	blk, err := NewBlockFromBuffer(b.currSBN, slice, blockLen, oti)
	if err != nil {
		return err
	}
	b.blocks = append(b.blocks, blk)
	b.currSBN++
	b.readEnd = (offsetEnd == len(content))
	b.currContentOffset = uint64(offsetEnd)
	return nil
}

func (b *BlockEncoder) readBlockStream() error {
	oti := &b.file.Oti
	var blockLen uint64
	if uint64(b.currSBN) < b.nbALarge {
		blockLen = b.aLarge
	} else {
		blockLen = b.aSmall
	}

	buf := make([]byte, int(blockLen)*int(oti.EncodingSymbolLength))

	// 串行读取底层流，期间持锁
	b.file.Object.Source.streamMu.Lock()
	n, err := b.file.Object.Source.stream.Read(buf)
	b.file.Object.Source.streamMu.Unlock()

	if err != nil {
		if errors.Is(err, io.EOF) {
			b.readEnd = true
			return nil
		}
		b.readEnd = true
		return err
	}
	if n == 0 {
		b.readEnd = true
		return nil
	}
	buf = buf[:n]

	blk, err := NewBlockFromBuffer(b.currSBN, buf, blockLen, oti)
	if err != nil {
		return err
	}
	b.blocks = append(b.blocks, blk)
	b.currSBN++
	b.currContentOffset += uint64(len(buf))
	return nil
}
