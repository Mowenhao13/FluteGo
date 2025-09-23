package sender

import (
	"Flute_go/pkg/alc"
	"Flute_go/pkg/profile"
	"Flute_go/pkg/transport"
	t "Flute_go/pkg/type"
	"log"
	"time"
)

type SenderSession struct {
	Priority         uint32
	Endpoint         transport.UDPEndpoint
	TSI              uint64
	File             *FileDesc
	Encoder          *BlockEncoder
	InterleaveBlocks int
	TransferFdtOnly  bool
	Profile          profile.Profile
}

// NewSenderSession 构造函数
func NewSenderSession(priority uint32, tsi uint64, interleaveBlocks int, transferFdtOnly bool, profile profile.Profile, endpoint transport.UDPEndpoint) *SenderSession {
	return &SenderSession{
		Priority:         priority,
		Endpoint:         endpoint,
		TSI:              tsi,
		File:             nil,
		Encoder:          nil,
		InterleaveBlocks: interleaveBlocks,
		TransferFdtOnly:  transferFdtOnly,
		Profile:          profile,
	}
}

func (s *SenderSession) Run(fdt *Fdt, now time.Time) []byte {
	for {
		// 1) 若 encoder 为空，尝试获取新文件/新编码器
		if s.Encoder == nil {
			s.getNext(fdt, now)
			// getNext 失败就直接返回 nil（没有可发送的包）
			if s.Encoder == nil || s.File == nil {
				return nil
			}
		}

		// 2) 非 FDT 专用会话：若需要发新的 FDT，停止发数据文件
		if !s.TransferFdtOnly {
			// Stop emitting packets if a new FDT is needed
			if fdt.NeedTransferFDT() {
				return nil
			}
		}

		// 3) 这里 **必须** 再判一次 s.file/s.encoder，避免 nil
		if s.File == nil || s.Encoder == nil {
			return nil
		}

		encoder := s.Encoder

		file := s.File

		// 如果允许随时停止且文件已经从 FDT 移除，则停止
		mustStopTransfer := !s.TransferFdtOnly &&
			file.CanTransferBeStopped() &&
			!fdt.IsAdded(file.TOI.String())

		if mustStopTransfer {
			log.Printf("File has already been transferred and removed from the FDT, stop transfer %s",
				file.Object.ContentLocation)
		}

		// 若文件设置了“下次发送时间戳”，且时间未到，先返回 nil
		if ts, ok := file.NextTransferTimestamp(); ok && ts.After(now) {
			return nil
		}

		// 4) 读一个符号包
		pkt, err := encoder.Read(mustStopTransfer)
		if err != nil || pkt == nil {
			s.releaseFile(fdt, now)
			continue
		}

		// 5) 推进下一次发送时间戳
		file.IncNextTransferTimestamp()

		// 6) 封装为 ALC/LCT（注意 Toi 常量/CCI）
		return alc.NewAlcPkt(
			&file.Oti,
			t.Uint128{
				High: 0,
				Low:  0,
			},
			s.TSI,
			pkt,
			s.Profile,
			now,
		)
	}
}

// getNext 拉取下一个 FileDesc 并构建 BlockEncoder
func (s *SenderSession) getNext(fdt *Fdt, now time.Time) {
	s.Encoder = nil
	s.File = nil

	// 选择 FDT 还是普通文件
	if s.TransferFdtOnly {
		s.File = fdt.GetNextFdtTransfer(now)
	} else {
		s.File = fdt.GetNextFileTransfer(s.Priority, now)
	}

	if s.File == nil {
		return
	}

	file := s.File
	isLastTransfer := file.IsLastTransfer()
	encoder, err := NewBlockEncoder(file, s.InterleaveBlocks, isLastTransfer)
	if err != nil {
		log.Printf("Fail to open Block Encoder: %v", err)
		s.releaseFile(fdt, now)
		return
	}
	s.Encoder = encoder
}

// releaseFile 释放当前 File
func (s *SenderSession) releaseFile(fdt *Fdt, now time.Time) {
	if s.File != nil {
		fdt.TransferDone(s.File, now)
	}
	s.File = nil
	s.Encoder = nil
}
