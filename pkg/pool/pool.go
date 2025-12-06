package pool

import (
	constant "FluteGo/constant"
	utils "FluteGo/pkg/utils"
	"fmt"
	"log"

	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// 连接包装器，包含连接和统计信息
type UDPConnWrapper struct {
	Mode      uint8 // 0-send 1-recv
	Conn      *net.UDPConn
	LastUsed  int64 // 最后使用时间戳
	IsHealthy bool
	Buffer    []byte // 每个连接专用的缓冲区
	FdtID     uint8
	mu        sync.RWMutex
	LastSent  int64
	sentData  uint32
}

func (w *UDPConnWrapper) MarkSent() {
	atomic.StoreInt64(&w.LastSent, time.Now().UnixNano())
	atomic.StoreUint32(&w.sentData, 1)
}

func (w *UDPConnWrapper) HadSent() bool {
	return atomic.LoadUint32(&w.sentData) == 1
}

// 高性能UDP连接池
type GlobalConnectionPool struct {
	Mode           uint8
	FileConns      sync.Map      // key: FdtID -> []*UDPConnWrapper (一个FdtID对应多个连接)
	Connections    sync.Map      // key: "ip:port" -> *UDPConnWrapper
	maxConns       int           // 每个目标的最大连接数
	ConnTimeout    time.Duration // 连接超时时间
	lastMetaRecv   int64
	Received       uint64
	fileChunks     sync.Map
	fileMd5Matched sync.Map
	stopChan       chan struct{}
	DestIP         string
}

// 连接池统计信息
type PoolStats struct {
	Mode           uint8
	TotalConns     int32
	ActiveConns    int32
	CreatedConns   int32
	DestroyedConns int32
	LastPort       int16
}

var (
	globalPool *GlobalConnectionPool
	poolOnce   sync.Once
	stats      PoolStats
)

func (g *GlobalConnectionPool) isInitialized() bool {
	return g != nil
}

func (g *GlobalConnectionPool) AddReceived(n uint64) {
	if n == 0 {
		return
	}
	atomic.AddUint64(&g.Received, n)
}

func (g *GlobalConnectionPool) ReceivedBytes() uint64 {
	return atomic.LoadUint64(&g.Received)
}

type chunkProgress struct {
	expected uint32
	written  uint32
}

func (g *GlobalConnectionPool) SetChunkTarget(fdtID uint8, target uint32) {
	if fdtID == 0 || target == 0 {
		return
	}
	if _, ok := g.fileChunks.Load(fdtID); ok {
		return
	}
	g.fileChunks.Store(fdtID, &chunkProgress{expected: target})
}

func (g *GlobalConnectionPool) MarkChunkWritten(fdtID uint8) uint32 {
	value, ok := g.fileChunks.Load(fdtID)
	if !ok {
		return 0
	}
	cp := value.(*chunkProgress)
	return atomic.AddUint32(&cp.written, 1)
}

func (g *GlobalConnectionPool) ChunkTargetReached(fdtID uint8) bool {
	value, ok := g.fileChunks.Load(fdtID)
	if !ok {
		return false
	}
	cp := value.(*chunkProgress)
	expected := atomic.LoadUint32(&cp.expected)
	if expected == 0 {
		return false
	}
	return atomic.LoadUint32(&cp.written) >= expected
}

func (g *GlobalConnectionPool) MarkFileMd5Matched(fdtID uint8) {
	if fdtID == 0 {
		return
	}
	g.fileMd5Matched.Store(fdtID, true)
}

func (g *GlobalConnectionPool) IsFileMd5Matched(fdtID uint8) bool {
	if fdtID == 0 {
		return false
	}
	if value, ok := g.fileMd5Matched.Load(fdtID); ok {
		matched, _ := value.(bool)
		return matched
	}
	return false
}

// 初始化全局连接池
func InitGlobalConnectionPool(maxConns int, timeout time.Duration, mode uint8, destIP string) {
	poolOnce.Do(func() {
		globalPool = &GlobalConnectionPool{
			Mode:        mode,
			maxConns:    maxConns,
			ConnTimeout: timeout,
			DestIP:     destIP,
			stopChan:    make(chan struct{}),
		}
		stats.LastPort = constant.MetaPort
		// 启动连接健康检查
		go globalPool.healthCheck()
		go globalPool.idleSenderMonitor()
	})
}

func (g *GlobalConnectionPool) InitMetaConn() ([]*UDPConnWrapper, []error) {
	return g.CreateNewFileConn(0, 1)
}

// 获取全局连接池实例
func GetGlobalPool() *GlobalConnectionPool {
	return globalPool
}

// 改进的获取连接方法
func (g *GlobalConnectionPool) GetGlobalConnection(destIP string, port int) (*UDPConnWrapper, error) {
	key := net.JoinHostPort(destIP, fmt.Sprintf("%d", port))

	// 尝试从池中获取健康连接
	if wrapper, ok := g.getHealthyConnection(key); ok {
		atomic.StoreInt64(&wrapper.LastUsed, time.Now().Unix())
		atomic.AddInt32(&stats.ActiveConns, 1)
		return wrapper, nil
	}

	// 创建新连接
	return g.createNewConnection(key, port)
}

func (g *GlobalConnectionPool) GetMetaConn() (*UDPConnWrapper, error) {
	_, conns, err := g.GetGlobalFileConn(0)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
		return nil, fmt.Errorf("no meta connections available")
	}
	return conns[0], nil
}

// 获取文件相关的所有连接
func (g *GlobalConnectionPool) GetGlobalFileConn(fdtID uint8) (uint16, []*UDPConnWrapper, error) {
	if value, ok := g.FileConns.Load(fdtID); ok {
		wrappers := value.([]*UDPConnWrapper)

		// 过滤出健康的连接
		var healthyConns []*UDPConnWrapper
		for _, wrapper := range wrappers {
			if wrapper.IsHealthy && g.isConnectionValid(wrapper) {
				atomic.StoreInt64(&wrapper.LastUsed, time.Now().Unix())
				healthyConns = append(healthyConns, wrapper)

			} else {
				// 移除不健康的连接
				g.removeUnhealthyFileConn(fdtID, wrapper)
			}
		}

		if len(healthyConns) == 0 {
			return 0, nil, fmt.Errorf("no healthy connections for fdtID %d", fdtID)
		}

		// 按 LocalAddr.Port 升序排序
		sort.Slice(healthyConns, func(i, j int) bool {
			addrI := healthyConns[i].Conn.LocalAddr().(*net.UDPAddr)
			addrJ := healthyConns[j].Conn.LocalAddr().(*net.UDPAddr)
			return addrI.Port < addrJ.Port
		})

		// 第一个连接的端口即为最小端口
		miniPort := uint16(healthyConns[0].Conn.LocalAddr().(*net.UDPAddr).Port)

		atomic.AddInt32(&stats.ActiveConns, int32(len(healthyConns)))
		return miniPort, healthyConns, nil
	}

	return 0, nil, fmt.Errorf("no connections found for fdtID %d", fdtID)
}

// 获取健康连接
func (g *GlobalConnectionPool) getHealthyConnection(key string) (*UDPConnWrapper, bool) {
	if value, ok := g.Connections.Load(key); ok {
		wrapper := value.(*UDPConnWrapper)
		if wrapper.IsHealthy && g.isConnectionValid(wrapper) {
			return wrapper, true
		}
		// 连接不健康，从池中移除
		g.Connections.Delete(key)
		atomic.AddInt32(&stats.DestroyedConns, 1)
		atomic.AddInt32(&stats.TotalConns, -1)
		wrapper.Conn.Close()
	}
	return nil, false
}

// 获取健康的文件连接（单个连接）
func (g *GlobalConnectionPool) getHealthyFileConn(fdtID uint8) (*UDPConnWrapper, bool) {
	if value, ok := g.FileConns.Load(fdtID); ok {
		wrappers := value.([]*UDPConnWrapper)
		for _, wrapper := range wrappers {
			if wrapper.IsHealthy && g.isConnectionValid(wrapper) {
				return wrapper, true
			}
		}
	}
	return nil, false
}

// 移除不健康的文件连接
func (g *GlobalConnectionPool) removeUnhealthyFileConn(fdtID uint8, targetWrapper *UDPConnWrapper) {
	if value, ok := g.FileConns.Load(fdtID); ok {
		existingWrappers := value.([]*UDPConnWrapper)
		var newWrappers []*UDPConnWrapper

		for _, wrapper := range existingWrappers {
			if wrapper != targetWrapper {
				newWrappers = append(newWrappers, wrapper)
			}
		}

		if len(newWrappers) == 0 {
			g.FileConns.Delete(fdtID)
		} else {
			g.FileConns.Store(fdtID, newWrappers)
		}

		// 关闭连接
		targetWrapper.mu.Lock()
		if targetWrapper.IsHealthy {
			targetWrapper.IsHealthy = false
			targetWrapper.Conn.Close()
			atomic.AddInt32(&stats.TotalConns, -1)
			atomic.AddInt32(&stats.DestroyedConns, 1)
		}
		targetWrapper.mu.Unlock()
	}
}

// 创建新连接
func (g *GlobalConnectionPool) createNewConnection(key string, port int) (*UDPConnWrapper, error) {
	var wrapper *UDPConnWrapper
	if g.Mode == 0 {
		Conn, err := utils.CreateUDPConnection(key)
		if err != nil {
			return nil, fmt.Errorf("创建UDP连接失败: %w", err)
		}

		// 优化连接参数
		g.optimizeConnection(Conn)
		wrapper = &UDPConnWrapper{
			Conn:      Conn,
			LastUsed:  time.Now().Unix(),
			IsHealthy: true,
			Buffer:    make([]byte, 1500), // MTU大小的专用缓冲区
		}
	} else {
		Conn, err := utils.CreateUDPListener(key)
		if err != nil {
			return nil, fmt.Errorf("监听UDP连接失败: %w", err)
		}
		// 优化连接参数
		g.optimizeConnection(Conn)
		wrapper = &UDPConnWrapper{
			Conn:      Conn,
			LastUsed:  time.Now().Unix(),
			IsHealthy: true,
			Buffer:    make([]byte, 1500), // MTU大小的专用缓冲区
		}
	}

	g.Connections.Store(key, wrapper)
	atomic.AddInt32(&stats.TotalConns, 1)
	atomic.AddInt32(&stats.CreatedConns, 1)
	atomic.AddInt32(&stats.ActiveConns, 1)
	stats.LastPort = max(stats.LastPort, int16(port))

	return wrapper, nil
}

// 为文件创建多个连接
func (g *GlobalConnectionPool) CreateNewFileConn(fdtID uint8, numConn uint8) ([]*UDPConnWrapper, []error) {
	return g.CreateNewFileConnWithBasePort(fdtID, numConn, int(stats.LastPort)+1)
}

// 指定起始端口为文件创建多个连接
func (g *GlobalConnectionPool) CreateNewFileConnWithBasePort(fdtID uint8, numConn uint8, basePort int) ([]*UDPConnWrapper, []error) {
	var conns []*UDPConnWrapper
	var errors []error

	for i := 0; i < int(numConn); i++ {
		port := basePort + i
		key := net.JoinHostPort(g.DestIP, fmt.Sprintf("%d", port))

		// Try to reuse existing connection
		if wrapper, ok := g.getHealthyConnection(key); ok {
			wrapper.FdtID = fdtID
			conns = append(conns, wrapper)
			fmt.Printf("Reused connection for fdtID(%d): %s:%d\n", fdtID, g.DestIP, port)
			continue
		}

		conn, err := g.createNewConnection(key, port)
		if err != nil {
			errors = append(errors, err)
			continue
		}

		// 设置FdtID
		conn.FdtID = fdtID
		conns = append(conns, conn)
		fmt.Printf("Created connection for fdtID(%d): %s:%d\n", fdtID, g.DestIP, port)
	}

	if len(conns) > 0 {
		// 将所有连接关联到FdtID
		g.FileConns.Store(fdtID, conns)
	}

	if len(errors) > 0 {
		if len(conns) > 0 {
			return conns, errors
		} else {
			return nil, errors
		}
	}
	return conns, nil
}

// 优化连接参数
func (g *GlobalConnectionPool) optimizeConnection(Conn *net.UDPConn) {
	// 设置发送/接收缓冲区大小
	Conn.SetReadBuffer(128 * 1024 * 1024)  // 128MB
	Conn.SetWriteBuffer(128 * 1024 * 1024) // 128MB

	// 默认不设置固定超时，避免长时间运行后触发过期 deadline
	var zero time.Time
	Conn.SetReadDeadline(zero)
	Conn.SetWriteDeadline(zero)
}

// 检查连接有效性
func (g *GlobalConnectionPool) isConnectionValid(wrapper *UDPConnWrapper) bool {
	// 检查连接是否超时（30秒未使用）
	if time.Since(time.Unix(atomic.LoadInt64(&wrapper.LastUsed), 0)) > 30*time.Second {
		return false
	}

	// 可以添加更多健康检查逻辑
	return wrapper.IsHealthy
}

// 健康检查协程
func (g *GlobalConnectionPool) healthCheck() {
	ticker := time.NewTicker(60 * time.Second) // 每分钟检查一次
	defer ticker.Stop()

	for {
		select {
		case <-g.stopChan:
			return
		case <-ticker.C:
			// 检查普通连接
			g.Connections.Range(func(key, value interface{}) bool {
				wrapper := value.(*UDPConnWrapper)

				if !g.isConnectionValid(wrapper) {
					g.Connections.Delete(key)
					atomic.AddInt32(&stats.TotalConns, -1)
					atomic.AddInt32(&stats.DestroyedConns, 1)
					wrapper.Conn.Close()
				}

				return true
			})

			// 检查文件连接
			g.FileConns.Range(func(key, value interface{}) bool {
				wrappers := value.([]*UDPConnWrapper)
				var healthyWrappers []*UDPConnWrapper

				for _, wrapper := range wrappers {
					if g.isConnectionValid(wrapper) {
						healthyWrappers = append(healthyWrappers, wrapper)
					} else {
						// 关闭不健康的连接
						wrapper.mu.Lock()
						if wrapper.IsHealthy {
							wrapper.IsHealthy = false
							wrapper.Conn.Close()
							atomic.AddInt32(&stats.TotalConns, -1)
							atomic.AddInt32(&stats.DestroyedConns, 1)
						}
						wrapper.mu.Unlock()
					}
				}

				if len(healthyWrappers) == 0 {
					g.FileConns.Delete(key)
				} else if len(healthyWrappers) != len(wrappers) {
					g.FileConns.Store(key, healthyWrappers)
				}

				return true
			})
		}
	}
}

// 归还连接到池中（实际UDP连接通常不需要归还，这里主要是更新状态）
func (g *GlobalConnectionPool) ReturnConnection(wrapper *UDPConnWrapper) {
	atomic.StoreInt64(&wrapper.LastUsed, time.Now().Unix())
	atomic.AddInt32(&stats.ActiveConns, -1)
}

// 关闭指定的连接
func (g *GlobalConnectionPool) CloseConnection(destIP string, port int) {
	key := net.JoinHostPort(destIP, fmt.Sprintf("%d", port))

	if value, ok := g.Connections.Load(key); ok {
		wrapper := value.(*UDPConnWrapper)

		// 使用读锁检查连接状态
		wrapper.mu.RLock()
		if !wrapper.IsHealthy {
			wrapper.mu.RUnlock()
			return // 连接已关闭或不健康，直接返回
		}
		wrapper.mu.RUnlock()

		// 使用写锁关闭连接
		wrapper.mu.Lock()
		if wrapper.IsHealthy {
			// 只在连接健康时关闭
			wrapper.IsHealthy = false
			wrapper.Conn.Close()
			g.Connections.Delete(key)
			atomic.AddInt32(&stats.TotalConns, -1)
			atomic.AddInt32(&stats.DestroyedConns, 1)

			// 如果有活跃连接数，减少计数
			active := atomic.LoadInt32(&stats.ActiveConns)
			if active > 0 {
				atomic.AddInt32(&stats.ActiveConns, -1)
			}
		}
		wrapper.mu.Unlock()
	}
	// 如果连接不存在，函数会静默返回，不执行任何操作
}

func (g *GlobalConnectionPool) CloseMetaConn() {
	metaConn, err := g.GetMetaConn()
	if err != nil || metaConn == nil || metaConn.Conn == nil {
		if err != nil {
			log.Printf("CloseMetaConn skipped: %v\n", err)
		} else {
			log.Printf("CloseMetaConn skipped: meta connection unavailable\n")
		}
		g.CloseFileConn(0)
		return
	}

	metaConn.Conn.Close()
	g.CloseFileConn(0)
}

// 关闭文件相关的所有连接
func (g *GlobalConnectionPool) CloseFileConn(fdtID uint8) {
	if value, ok := g.FileConns.Load(fdtID); ok {
		wrappers := value.([]*UDPConnWrapper)

		// 关闭所有相关连接
		for _, wrapper := range wrappers {
			wrapper.mu.Lock()
			if wrapper.IsHealthy {
				wrapper.IsHealthy = false
				wrapper.Conn.Close()
				atomic.AddInt32(&stats.TotalConns, -1)
				atomic.AddInt32(&stats.DestroyedConns, 1)
			}
			wrapper.mu.Unlock()
		}

		// 从池中移除
		g.FileConns.Delete(fdtID)

		// 更新活跃连接数
		active := atomic.LoadInt32(&stats.ActiveConns)
		if active >= int32(len(wrappers)) {
			atomic.AddInt32(&stats.ActiveConns, -int32(len(wrappers)))
		}
	}
}

func (g *GlobalConnectionPool) CloseAllFileConn() {
	var fdtIDs []uint8
	g.FileConns.Range(func(key, value interface{}) bool {
		if fdtID, ok := key.(uint8); ok {
			fdtIDs = append(fdtIDs, fdtID)
		}
		return true
	})
	for _, fdtID := range fdtIDs {
		g.CloseFileConn(fdtID)
	}
}

// 获取连接池统计信息
func (g *GlobalConnectionPool) GetStats() PoolStats {
	return PoolStats{
		TotalConns:     atomic.LoadInt32(&stats.TotalConns),
		ActiveConns:    atomic.LoadInt32(&stats.ActiveConns),
		CreatedConns:   atomic.LoadInt32(&stats.CreatedConns),
		DestroyedConns: atomic.LoadInt32(&stats.DestroyedConns),
		LastPort:       stats.LastPort,
	}
}

func (g *GlobalConnectionPool) ShowInfo() {
	totalConns := g.GetStats().TotalConns
	activeConns := g.GetStats().ActiveConns
	createdConns := g.GetStats().CreatedConns
	destoryedConns := g.GetStats().DestroyedConns
	lastPort := g.GetStats().LastPort

	fmt.Printf("TotalConns: %d\n", totalConns)
	fmt.Printf("ActiveConns: %d\n", activeConns)
	fmt.Printf("CreatedConns: %d\n", createdConns)
	fmt.Printf("DestoryedConns: %d\n", destoryedConns)
	fmt.Printf("LastPort: %d\n", lastPort)
}

// 辅助函数：返回较大值
func max(a, b int16) int16 {
	if a > b {
		return a
	}
	return b
}

func (g *GlobalConnectionPool) MetaIdleDuration() time.Duration {
	last := atomic.LoadInt64(&g.lastMetaRecv)
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

func (g *GlobalConnectionPool) closeWrapperIdle(wrapper *UDPConnWrapper) {
	wrapper.mu.Lock()
	if wrapper.IsHealthy {
		wrapper.IsHealthy = false
		wrapper.Conn.Close()
		atomic.AddInt32(&stats.TotalConns, -1)
		atomic.AddInt32(&stats.DestroyedConns, 1)
	}
	wrapper.mu.Unlock()

	// 清理 FileConns
	g.removeUnhealthyFileConn(wrapper.FdtID, wrapper)

	// 从全局连接表中移除
	if wrapper.Conn != nil {
		if key := wrapper.Conn.RemoteAddr(); key != nil {
			g.Connections.Delete(key.String())
		}
	}

	active := atomic.LoadInt32(&stats.ActiveConns)
	if active > 0 {
		atomic.AddInt32(&stats.ActiveConns, -1)
	}

	atomic.StoreUint32(&wrapper.sentData, 0)
}

func (g *GlobalConnectionPool) idleSenderMonitor() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-g.stopChan:
			return
		case <-ticker.C:
			g.Connections.Range(func(key, value interface{}) bool {
				wrapper := value.(*UDPConnWrapper)
				if atomic.LoadUint32(&wrapper.sentData) == 0 {
					return true
				}
				last := atomic.LoadInt64(&wrapper.LastSent)
				if last == 0 {
					return true
				}
				if time.Since(time.Unix(0, last)) > 3*time.Second {
					log.Printf("idle close %s", key)
					g.closeWrapperIdle(wrapper)
				}
				return true
			})
		}
	}
}

func (g *GlobalConnectionPool) Stop() {
	close(g.stopChan)
}
