/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package receiver

import (
	"FluteGo/pkg/meta"
	"sync"
	"sync/atomic"
	"time"
)

// FDTReceiver 管理接收端的 FDT 实例
type FDTReceiver struct {
	mu sync.RWMutex
	
	// 当前 FDT 实例
	currentFDT *meta.FDTInstance
	
	// 已知的 FDT ID 和版本
	knownFDTs map[uint32]uint32 // FdtID -> Version
	
	// 文件接收状态
	fileStates map[uint32]*FileState // TOI -> FileState
	
	// 是否正在运行
	running int32
	
	// 更新回调
	onFileAdded func(toi uint32, file meta.FDTFile)
	onFileRemoved func(toi uint32)
	onFileUpdated func(toi uint32, file meta.FDTFile)
}

// FileState 文件接收状态
type FileState struct {
	TOI          uint32
	File         meta.FDTFile
	ReceivedBytes int64
	TotalBytes   int64
	Status       uint8 // 0-未开始, 1-接收中, 2-已完成, 3-失败
	StartTime    time.Time
	CompleteTime time.Time
	CacheControl meta.ObjectCacheControl // 缓存控制策略
}

// NewFDTReceiver 创建 FDT 接收管理器
func NewFDTReceiver() *FDTReceiver {
	return &FDTReceiver{
		knownFDTs:  make(map[uint32]uint32),
		fileStates: make(map[uint32]*FileState),
	}
}

// Start 启动 FDT 接收管理器
func (r *FDTReceiver) Start() {
	atomic.StoreInt32(&r.running, 1)
}

// Stop 停止 FDT 接收管理器
func (r *FDTReceiver) Stop() {
	atomic.StoreInt32(&r.running, 0)
}

// SetCallbacks 设置回调函数
func (r *FDTReceiver) SetCallbacks(
	onAdded func(toi uint32, file meta.FDTFile),
	onRemoved func(toi uint32),
	onUpdated func(toi uint32, file meta.FDTFile),
) {
	r.onFileAdded = onAdded
	r.onFileRemoved = onRemoved
	r.onFileUpdated = onUpdated
}

// ProcessFDT 处理接收到的 FDT 实例
func (r *FDTReceiver) ProcessFDT(fdt *meta.FDTInstance) error {
	if fdt == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 检查是否是新的或更新的 FDT
	if version, exists := r.knownFDTs[fdt.FdtID]; exists {
		if fdt.Version <= version {
			// 旧版本或相同版本，忽略
			return nil
		}
	}

	// 更新已知 FDT 版本
	r.knownFDTs[fdt.FdtID] = fdt.Version

	// 保存当前 FDT
	r.currentFDT = fdt

	// 处理文件列表
	r.processFiles(fdt)

	return nil
}

// processFiles 处理文件列表变化
func (r *FDTReceiver) processFiles(fdt *meta.FDTInstance) {
	// 创建新的 TOI 集合
	newTOIs := make(map[uint32]bool)
	for _, file := range fdt.Files {
		newTOIs[file.TOI] = true

		// 检查文件是否已存在
		if state, exists := r.fileStates[file.TOI]; exists {
			// 文件已存在，检查是否需要更新
			if r.isFileChanged(state.File, file) {
				// 文件内容变化，触发更新
				state.File = file
				state.Status = 0 // 重置状态
				state.ReceivedBytes = 0
				state.StartTime = time.Time{}
				state.CompleteTime = time.Time{}
				if r.onFileUpdated != nil {
					r.onFileUpdated(file.TOI, file)
				}
			} else {
				// 文件未变化，检查缓存控制是否需要更新
				r.updateCacheControl(state, file, fdt.Expires)
			}
		} else {
			// 新文件，触发添加
			cacheControl := meta.ParseCacheControl(file.CacheControl, time.Unix(int64(fdt.Expires), 0))
			r.fileStates[file.TOI] = &FileState{
				TOI:          file.TOI,
				File:         file,
				TotalBytes:   int64(file.TransferLength),
				Status:       0,
				CacheControl: cacheControl,
			}
			if r.onFileAdded != nil {
				r.onFileAdded(file.TOI, file)
			}
		}
	}

	// 检查是否有文件被移除
	for toi := range r.fileStates {
		if !newTOIs[toi] {
			// 文件被移除
			if r.onFileRemoved != nil {
				r.onFileRemoved(toi)
			}
			delete(r.fileStates, toi)
		}
	}
}

// isFileChanged 判断文件是否发生变化
// 参考 ref/flute 的实现，通过 ETag 和 MD5 判断
func (r *FDTReceiver) isFileChanged(oldFile, newFile meta.FDTFile) bool {
	// 1. 检查 ETag（如果存在）
	if oldFile.FileETag != "" && newFile.FileETag != "" {
		return oldFile.FileETag != newFile.FileETag
	}

	// 2. 检查 MD5（如果存在）
	if oldFile.ContentMD5 != "" && newFile.ContentMD5 != "" {
		return oldFile.ContentMD5 != newFile.ContentMD5
	}

	// 3. 检查文件大小
	if oldFile.TransferLength != newFile.TransferLength {
		return true
	}

	// 4. 如果以上都无法判断，认为文件未变化
	return false
}

// updateCacheControl 更新缓存控制
func (r *FDTReceiver) updateCacheControl(state *FileState, file meta.FDTFile, fdtExpires uint32) {
	newCacheControl := meta.ParseCacheControl(file.CacheControl, time.Unix(int64(fdtExpires), 0))
	if state.CacheControl.ShouldUpdate(newCacheControl) {
		state.CacheControl = newCacheControl
	}
}

// UpdateFileState 更新文件接收状态
func (r *FDTReceiver) UpdateFileState(toi uint32, receivedBytes int64, status uint8) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if state, exists := r.fileStates[toi]; exists {
		state.ReceivedBytes = receivedBytes
		state.Status = status
		
		if status == 1 && state.StartTime.IsZero() {
			state.StartTime = time.Now()
		}
		if status == 2 && state.CompleteTime.IsZero() {
			state.CompleteTime = time.Now()
		}
	}
}

// GetFileState 获取文件状态
func (r *FDTReceiver) GetFileState(toi uint32) *FileState {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if state, exists := r.fileStates[toi]; exists {
		// 返回副本
		stateCopy := *state
		return &stateCopy
	}
	return nil
}

// GetAllFileStates 获取所有文件状态
func (r *FDTReceiver) GetAllFileStates() map[uint32]*FileState {
	r.mu.RLock()
	defer r.mu.RUnlock()

	states := make(map[uint32]*FileState)
	for toi, state := range r.fileStates {
		stateCopy := *state
		states[toi] = &stateCopy
	}
	return states
}

// GetCurrentFDT 获取当前 FDT
func (r *FDTReceiver) GetCurrentFDT() *meta.FDTInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.currentFDT == nil {
		return nil
	}

	// 返回副本
	fdtCopy := *r.currentFDT
	return &fdtCopy
}

// GetFileCount 获取文件数量
func (r *FDTReceiver) GetFileCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.fileStates)
}

// GetCompletedCount 获取已完成文件数量
func (r *FDTReceiver) GetCompletedCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, state := range r.fileStates {
		if state.Status == 2 {
			count++
		}
	}
	return count
}
