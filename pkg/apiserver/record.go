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
	SpeedMbps float64 `json:"speedMbps"` // sliding window rate (1 s)
	FECType   string  `json:"fecType"`   // "NoCode"|"RaptorQ"|"ReedSolomon"
	MD5       string  `json:"md5"`
	Status    string  `json:"status"` // "pending"|"transferring"|"completed"|"failed"
}

// StateStore maintains in-memory snapshots of all active transfers and
// broadcasts state changes to the WebSocket hub.
type StateStore struct {
	mu            sync.RWMutex
	records       map[uint8]*TransferRecord
	prevBytes     map[uint8]int64
	prevTime      map[uint8]time.Time
	lastBroadcast map[uint8]time.Time
	hub           *Hub
}

func newStateStore(hub *Hub) *StateStore {
	return &StateStore{
		records:       make(map[uint8]*TransferRecord),
		prevBytes:     make(map[uint8]int64),
		prevTime:      make(map[uint8]time.Time),
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
	s.prevBytes[fdtID] = 0
	s.prevTime[fdtID] = time.Now()
	rec := *s.records[fdtID]
	s.mu.Unlock()

	s.broadcastUpdate(rec)
}

// SenderProgressAdapter returns an onProgress func(int64) callback suitable
// for sender.SetProgressCallback. totalBytes should come from
// sender.GetTotalBytesToSend().
func (s *StateStore) SenderProgressAdapter(fdtID uint8, totalBytes int64) func(int64) {
	return func(bytesDone int64) {
		s.mu.Lock()
		rec, ok := s.records[fdtID]
		if !ok {
			s.mu.Unlock()
			return
		}
		rec.BytesDone = bytesDone
		if totalBytes > 0 {
			rec.Progress = float64(bytesDone) / float64(totalBytes)
			if rec.Progress > 1.0 {
				rec.Progress = 1.0
			}
		}
		now := time.Now()
		dt := now.Sub(s.prevTime[fdtID]).Seconds()
		if dt > 0 {
			db := bytesDone - s.prevBytes[fdtID]
			rec.SpeedMbps = float64(db) * 8.0 / dt / 1e6
		}
		if bytesDone > 0 && rec.Status == "pending" {
			rec.Status = "transferring"
		}
		if rec.Progress >= 1.0 {
			rec.Status = "completed"
		}
		s.prevBytes[fdtID] = bytesDone
		s.prevTime[fdtID] = now
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
	now := time.Now()
	dt := now.Sub(s.prevTime[fdtID]).Seconds()
	if dt > 0 {
		db := received - s.prevBytes[fdtID]
		rec.SpeedMbps = float64(db) * 8.0 / dt / 1e6
	}
	switch status {
	case 0:
		rec.Status = "transferring"
	case 1:
		rec.Status = "completed"
	case 2:
		rec.Status = "failed"
	}
	s.prevBytes[fdtID] = received
	s.prevTime[fdtID] = now
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

	now := time.Now()
	last, ok := s.lastBroadcast[rec.FdtID]

	// 总是广播完成或失败状态，其他状态最多每 50ms 广播一次（20fps）
	shouldBroadcast := !ok ||
		rec.Status == "completed" ||
		rec.Status == "failed" ||
		now.Sub(last) >= 50*time.Millisecond

	if shouldBroadcast {
		s.lastBroadcast[rec.FdtID] = now
		msg := encodeWSMsg("update", rec)
		if msg != nil {
			s.hub.Broadcast(msg)
		}
	}
}
