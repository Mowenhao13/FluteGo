/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package sender

import (
	"FluteGo/pkg/meta"
	"sync"
	"sync/atomic"
	"time"
)

// FDTManager 管理 FDT 实例的增量更新
type FDTManager struct {
	mu sync.RWMutex
	
	// 当前 FDT 实例
	currentFDT *meta.FDTInstance
	
	// FDT ID 计数器 (20-bit, 0~0xFFFFF)
	fdtIDCounter uint32
	
	// 版本计数器
	version uint32
	
	// 是否正在运行
	running int32
	
	// 更新间隔
	updateInterval time.Duration
	
	// 更新通道
	updateChan chan struct{}
	
	// 发送函数
	sendFDT func(*meta.FDTInstance) error
}

// NewFDTManager 创建 FDT 管理器
func NewFDTManager(updateInterval time.Duration, sendFDT func(*meta.FDTInstance) error) *FDTManager {
	return &FDTManager{
		fdtIDCounter:   1,
		version:        1,
		updateInterval: updateInterval,
		updateChan:     make(chan struct{}, 10),
		sendFDT:        sendFDT,
	}
}

// Start 启动 FDT 管理器
func (m *FDTManager) Start() {
	if atomic.CompareAndSwapInt32(&m.running, 0, 1) {
		go m.updateLoop()
	}
}

// Stop 停止 FDT 管理器
func (m *FDTManager) Stop() {
	if atomic.CompareAndSwapInt32(&m.running, 1, 0) {
		close(m.updateChan)
	}
}

// AddFile 添加文件到 FDT
func (m *FDTManager) AddFile(file meta.FDTFile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.currentFDT == nil {
		m.currentFDT = &meta.FDTInstance{
			XMLNS:    meta.FDTNamespace,
			Expires:  uint32(time.Now().Add(24 * time.Hour).Unix()),
			Complete: true,
		}
	}
	
	m.currentFDT.AddFile(file)
	m.triggerUpdate()
}

// RemoveFile 从 FDT 中移除文件
func (m *FDTManager) RemoveFile(toi uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.currentFDT != nil {
		m.currentFDT.RemoveFile(toi)
		m.triggerUpdate()
	}
}

// UpdateFile 更新文件信息
func (m *FDTManager) UpdateFile(file meta.FDTFile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.currentFDT != nil {
		// 先移除旧文件
		m.currentFDT.RemoveFile(file.TOI)
		// 再添加新文件
		m.currentFDT.AddFile(file)
		m.triggerUpdate()
	}
}

// triggerUpdate 触发 FDT 更新
func (m *FDTManager) triggerUpdate() {
	select {
	case m.updateChan <- struct{}{}:
	default:
		// 通道已满,跳过
	}
}

// updateLoop 更新循环
func (m *FDTManager) updateLoop() {
	ticker := time.NewTicker(m.updateInterval)
	defer ticker.Stop()
	
	for atomic.LoadInt32(&m.running) == 1 {
		select {
		case <-ticker.C:
			m.publishFDT()
		case <-m.updateChan:
			m.publishFDT()
		}
	}
}

// publishFDT 发布 FDT
func (m *FDTManager) publishFDT() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if m.currentFDT == nil || m.sendFDT == nil {
		return
	}
	
	// 创建 FDT 副本
	fdtCopy := *m.currentFDT
	fdtCopy.FdtID = atomic.AddUint32(&m.fdtIDCounter, 1) & 0xFFFFF
	fdtCopy.Version = atomic.AddUint32(&m.version, 1)
	
	// 发送 FDT
	if err := m.sendFDT(&fdtCopy); err != nil {
		// 记录错误但不中断
		return
	}
}

// GetCurrentFDT 获取当前 FDT
func (m *FDTManager) GetCurrentFDT() *meta.FDTInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if m.currentFDT == nil {
		return nil
	}
	
	// 返回副本
	fdtCopy := *m.currentFDT
	return &fdtCopy
}

// GetFileCount 获取文件数量
func (m *FDTManager) GetFileCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if m.currentFDT == nil {
		return 0
	}
	return m.currentFDT.FileCount()
}
