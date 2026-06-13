package apiserver

import (
	"sync"
	"time"
)

// TransferRecord is the unified transfer state snapshot sent to the frontend
// via REST and WebSocket.
type TransferRecord struct {
	FdtID     uint8   `json:"fdtId"`
	Role      string  `json:"role"`      // "sender" | "receiver"
	FileName  string  `json:"fileName"`
	FileSize  int64   `json:"fileSize"`
	BytesDone int64   `json:"bytesDone"`
	Progress  float64 `json:"progress"`  // 0.0–1.0
	SpeedMbps float64 `json:"speedMbps"` // average speed in Mbps
	FECType   string  `json:"fecType"`   // "NoCode"|"RaptorQ"|"ReedSolomon"
	MD5       string  `json:"md5"`
	Status    string  `json:"status"` // "pending"|"transferring"|"completed"|"failed"
}

// StateStore maintains in-memory snapshots of all active transfers and
// broadcasts state changes to the WebSocket hub.
type StateStore struct {
	mu            sync.RWMutex
	records       map[uint8]*TransferRecord
	startTime     map[uint8]time.Time // 记录每个文件注册/开始时间
	dataStartTime map[uint8]time.Time // 记录首次实际数据传输的时间（用于速率计算）
	lastBroadcast map[uint8]time.Time
	hub           *Hub
}

func newStateStore(hub *Hub) *StateStore {
	return &StateStore{
		records:       make(map[uint8]*TransferRecord),
		startTime:     make(map[uint8]time.Time),
		dataStartTime: make(map[uint8]time.Time),
		lastBroadcast: make(map[uint8]time.Time),
		hub:           hub,
	}
}

// RegisterFile registers a file at the start of a transfer.
func (s *StateStore) RegisterFile(role string, fdtID uint8, fileName string,
	fileSize int64, fecType, md5 string) {
	s.mu.Lock()
	s.records[fdtID] = &TransferRecord{
		FdtID:    fdtID,
		Role:     role,
		FileName: fileName,
		FileSize: fileSize,
		FECType:  fecType,
		MD5:      md5,
		Status:   "pending",
	}
	s.startTime[fdtID] = time.Now()
	rec := *s.records[fdtID]
	s.mu.Unlock()

	s.broadcastUpdate(rec)
}

// SenderProgressAdapter returns an onProgress func(int64) callback suitable
// for sender.SetProgressCallback. fileSize is the original file payload size;
// totalBytesWire is sender.GetTotalBytesToSend() which includes headers + FEC.
//
// The returned function translates wire bytes into estimated payload bytes
// so that progress and speedMbps are reported on the same basis as the receiver.
func (s *StateStore) SenderProgressAdapter(fdtID uint8, fileSize, totalBytesWire int64) func(int64) {
	return func(bytesDoneWire int64) {
		s.mu.Lock()
		rec, ok := s.records[fdtID]
		if !ok {
			s.mu.Unlock()
			return
		}

		// 将 wire 字节转换为估算的 payload 字节，使进度基准与接收端一致
		var payloadBytes int64
		if bytesDoneWire >= totalBytesWire {
			payloadBytes = fileSize // 发送完成 = 全部 payload 已覆盖
		} else if totalBytesWire > 0 && fileSize > 0 {
			payloadBytes = int64(float64(bytesDoneWire) * float64(fileSize) / float64(totalBytesWire))
		} else {
			payloadBytes = bytesDoneWire
		}
		if payloadBytes > fileSize {
			payloadBytes = fileSize
		}

		rec.BytesDone = payloadBytes
		if fileSize > 0 {
			rec.Progress = float64(payloadBytes) / float64(fileSize)
			if rec.Progress > 1.0 {
				rec.Progress = 1.0
			}
		}

		// 记录首次实际数据传输时间（用于速率计算，排除 START_SEND_WAIT 等延迟）
		now := time.Now()
		if bytesDoneWire > 0 {
			if _, hasData := s.dataStartTime[fdtID]; !hasData {
				s.dataStartTime[fdtID] = now
			}
		}

		// 计算速率：以 payload 字节为基准，使用 dataStartTime（回退到 startTime）
		rateStart, ok := s.dataStartTime[fdtID]
		if !ok {
			rateStart = s.startTime[fdtID]
		}
		dt := now.Sub(rateStart).Seconds()
		if dt > 0 {
			rec.SpeedMbps = float64(payloadBytes) * 8.0 / dt / 1e6
		}

		if bytesDoneWire > 0 && rec.Status == "pending" {
			rec.Status = "transferring"
		}
		if rec.Progress >= 1.0 {
			rec.Status = "completed"
		}
		snapshot := *rec
		s.mu.Unlock()

		s.broadcastUpdate(snapshot)
	}
}

// UpdateFromReceiverReport updates state from a receiver-side FileReport.
// status: 0 = transferring, 1 = completed, 2 = failed.
func (s *StateStore) UpdateFromReceiverReport(fdtID uint8, received, total int64, status uint8) {
	s.mu.Lock()
	rec, ok := s.records[fdtID]
	if !ok {
		s.mu.Unlock()
		return
	}
	rec.BytesDone = received
	if total > 0 {
		rec.Progress = float64(received) / float64(total)
		if rec.Progress > 1.0 {
			rec.Progress = 1.0
		}
	}
	// 记录首次实际接收数据时间
	now := time.Now()
	if received > 0 {
		if _, hasData := s.dataStartTime[fdtID]; !hasData {
			s.dataStartTime[fdtID] = now
		}
	}
	// 计算平均速率：以 payload 字节为基准，使用 dataStartTime（回退到 startTime）
	rateStart, ok := s.dataStartTime[fdtID]
	if !ok {
		rateStart = s.startTime[fdtID]
	}
	dt := now.Sub(rateStart).Seconds()
	if dt > 0 {
		rec.SpeedMbps = float64(received) * 8.0 / dt / 1e6
	}
	switch status {
	case 0:
		rec.Status = "transferring"
	case 1:
		rec.Status = "completed"
	case 2:
		rec.Status = "failed"
	}
	snapshot := *rec
	s.mu.Unlock()

	s.broadcastUpdate(snapshot)
}

// Snapshot returns a copy of all records (used by the REST endpoint).
func (s *StateStore) Snapshot() []TransferRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]TransferRecord, 0, len(s.records))
	for _, r := range s.records {
		result = append(result, *r)
	}
	return result
}

func (s *StateStore) broadcastUpdate(rec TransferRecord) {
	if s.hub == nil {
		return
	}

	s.mu.Lock()
	now := time.Now()
	last, ok := s.lastBroadcast[rec.FdtID]

	// 总是广播完成或失败状态，其他状态最多每 100ms 广播一次
	shouldBroadcast := !ok ||
		rec.Status == "completed" ||
		rec.Status == "failed" ||
		now.Sub(last) >= 100*time.Millisecond

	if shouldBroadcast {
		s.lastBroadcast[rec.FdtID] = now
	}
	s.mu.Unlock()

	if shouldBroadcast {
		msg := encodeWSMsg("update", rec)
		if msg != nil {
			s.hub.Broadcast(msg)
		}
	}
}
