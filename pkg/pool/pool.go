/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

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

// UDPConnWrapper UDP连接器
// 功能说明：
//
//	包装net.UDPConn连接，增加连接状态管理、统计信息和并发控制
//
// 设计模式：
//
//	装饰器模式，扩展原生UDP连接的功能
//
// 线程安全：
//
//	通过读写锁保护共享状态，通过原子操作更新统计信息
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

// MarkSent 标记连接已发送数据
// 功能说明：
//
//	更新连接的最后发送时间和数据发送状态
//
// 使用场景：
//
//	在成功发送数据后调用，用于空闲连接检测
//
// 性能优化：
//
//	使用原子操作避免锁竞争
func (w *UDPConnWrapper) MarkSent() {
	atomic.StoreInt64(&w.LastSent, time.Now().UnixNano())
	atomic.StoreUint32(&w.sentData, 1)
}

// HadSent 检查连接是否发送过数据
// 功能说明：
//
//	查询连接的发送状态，用于判断连接是否活跃
//
// 返回值：
//
//	bool - true表示连接已发送过数据，false表示未发送
func (w *UDPConnWrapper) HadSent() bool {
	return atomic.LoadUint32(&w.sentData) == 1
}

// GlobalConnectionPool 全局UDP连接池
// 功能说明：
//
//	管理所有UDP连接的创建、复用、健康检查和销毁
//
// 核心特性：
//   - 连接复用，减少连接创建开销
//   - 按FdtID分组管理连接
//   - 连接健康检查和自动清理
//   - 发送空闲连接监控
//
// 数据结构：
//   - FileConns: 按文件传输ID组织的连接组
//   - Connections: 按地址组织的全局连接映射
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

// PoolStats 连接池统计信息
// 功能说明：
//
//	记录连接池的运行状态和性能指标
//
// 监控指标：
//
//	TotalConns - 当前总连接数
//	ActiveConns - 活跃连接数
//	CreatedConns - 创建的总连接数
//	DestroyedConns - 销毁的总连接数
//	LastPort - 最后使用的端口号
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

// isInitialized 检查连接池是否已初始化
// 功能说明：
//
//	验证全局连接池实例是否已创建
//
// 返回值：
//
//	bool - true表示已初始化，false表示未初始化
//
// 使用场景：
//
//	在获取连接池实例前进行检查
func (g *GlobalConnectionPool) isInitialized() bool {
	return g != nil
}

// AddReceived 增加接收字节数统计
// 功能说明：
//
//	原子增加接收总字节数
//
// 参数：
//
//	n - 增加的字节数
//
// 线程安全：
//
//	使用atomic.AddUint64保证原子性
func (g *GlobalConnectionPool) AddReceived(n uint64) {
	if n == 0 {
		return
	}
	atomic.AddUint64(&g.Received, n)
}

// ReceivedBytes 获取接收字节数
// 功能说明：
//
//	获取当前接收的总字节数
//
// 返回值：
//
//	uint64 - 接收的总字节数
func (g *GlobalConnectionPool) ReceivedBytes() uint64 {
	return atomic.LoadUint64(&g.Received)
}

// chunkProgress 分块进度跟踪结构
// 功能说明：
//
//	跟踪单个文件的传输进度
//
// 字段说明：
//
//	expected - 预期的分块总数
//	written  - 已写入的分块数
type chunkProgress struct {
	expected uint32
	written  uint32
}

// SetChunkTarget 设置分块目标数
// 功能说明：
//
//	为指定文件传输设置预期的分块总数
//
// 参数：
//
//	fdtID  - 文件数据传输标识符
//	target - 预期分块总数
//
// 线程安全：
//
//	使用sync.Map保证并发安全
func (g *GlobalConnectionPool) SetChunkTarget(fdtID uint8, target uint32) {
	if fdtID == 0 || target == 0 {
		return
	}
	if _, ok := g.fileChunks.Load(fdtID); ok {
		return
	}
	g.fileChunks.Store(fdtID, &chunkProgress{expected: target})
}

// MarkChunkWritten 标记分块已写入
// 功能说明：
//
//	原子增加已写入分块计数
//
// 参数：
//
//	fdtID - 文件数据传输标识符
//
// 返回值：
//
//	uint32 - 更新后的已写入分块数
//
// 使用场景：
//
//	在每个分块成功写入文件后调用
func (g *GlobalConnectionPool) MarkChunkWritten(fdtID uint8) uint32 {
	value, ok := g.fileChunks.Load(fdtID)
	if !ok {
		return 0
	}
	cp := value.(*chunkProgress)
	return atomic.AddUint32(&cp.written, 1)
}

// ChunkTargetReached 检查分块目标是否达成
// 功能说明：
//
//	检查指定文件的已写入分块数是否达到预期目标
//
// 参数：
//
//	fdtID - 文件数据传输标识符
//
// 返回值：
//
//	bool - true表示目标已达成，false表示未达成
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

// MarkFileMd5Matched 标记文件MD5校验通过
// 功能说明：
//
//	记录指定文件的MD5校验已通过
//
// 参数：
//
//	fdtID - 文件数据传输标识符
//
// 使用场景：
//
//	在文件完整性验证通过后调用
func (g *GlobalConnectionPool) MarkFileMd5Matched(fdtID uint8) {
	if fdtID == 0 {
		return
	}
	g.fileMd5Matched.Store(fdtID, true)
}

// IsFileMd5Matched 检查文件MD5是否已匹配
// 功能说明：
//
//	查询指定文件的MD5校验状态
//
// 参数：
//
//	fdtID - 文件数据传输标识符
//
// 返回值：
//
//	bool - true表示MD5已匹配，false表示未匹配或未知
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

// InitGlobalConnectionPool 初始化全局连接池
// 功能说明：
//
//	单例模式初始化全局连接池实例
//
// 参数：
//
//	maxConns - 每个目标的最大连接数
//	timeout  - 连接超时时间
//	mode     - 连接池模式：0-发送，1-接收
//	destIP   - 目标IP地址
//
// 初始化步骤：
//  1. 创建连接池实例
//  2. 设置连接池参数
//  3. 启动健康检查协程
//  4. 启动空闲发送者监控协程
func InitGlobalConnectionPool(maxConns int, timeout time.Duration, mode uint8, destIP string) {
	poolOnce.Do(func() {
		globalPool = &GlobalConnectionPool{
			Mode:        mode,
			maxConns:    maxConns,
			ConnTimeout: timeout,
			DestIP:      destIP,
			stopChan:    make(chan struct{}),
		}
		stats.LastPort = constant.MetaPort
		// 启动连接健康检查
		go globalPool.healthCheck()
		go globalPool.idleSenderMonitor()
	})
}

// InitMetaConn 初始化元数据连接
// 功能说明：
//
//	为元数据传输创建专用连接
//
// 返回值：
//
//	[]*UDPConnWrapper - 创建的连接列表
//	[]error           - 创建过程中遇到的错误
//
// 特殊处理：
//
//	元数据连接使用特殊的FdtID(0)标识，使用固定的MetaPort (3399)
func (g *GlobalConnectionPool) InitMetaConn() ([]*UDPConnWrapper, []error) {
	return g.CreateNewFileConnWithBasePort(0, 1, constant.MetaPort)
}

// GetGlobalPool 获取全局连接池实例
// 功能说明：
//
//	获取单例模式的全局连接池实例
//
// 返回值：
//
//	*GlobalConnectionPool - 全局连接池实例
//
// 使用场景：
//
//	在整个应用程序中获取唯一的连接池实例
func GetGlobalPool() *GlobalConnectionPool {
	return globalPool
}

// GetGlobalConnection 获取全局连接
// 功能说明：
//
//	根据目标地址获取或创建UDP连接
//
// 参数：
//
//	destIP - 目标IP地址
//	port   - 目标端口号
//
// 返回值：
//
//	*UDPConnWrapper - UDP连接包装器
//	error           - 获取或创建过程中的错误
//
// 获取策略：
//  1. 尝试从池中获取健康连接
//  2. 如果无可用连接，创建新连接
//  3. 更新连接使用时间和活跃计数
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

// GetMetaConn 获取元数据连接
// 功能说明：
//
//	获取用于元数据传输的连接
//
// 返回值：
//
//	*UDPConnWrapper - 元数据连接包装器
//	error           - 获取过程中的错误
//
// 特殊处理：
//
//	元数据连接通常使用固定端口和FdtID(0)
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

// GetGlobalFileConn 获取文件相关连接
// 功能说明：
//
//	根据FdtID获取文件相关的所有连接
//
// 参数：
//
//	fdtID - 文件数据传输标识符
//
// 返回值：
//
//	uint16 - 最小端口号
//	[]*UDPConnWrapper - 连接列表
//	error - 获取过程中的错误
//
// 处理流程：
//  1. 从FileConns中获取连接列表
//  2. 过滤健康连接
//  3. 按端口号排序
//  4. 返回最小端口和连接列表
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

// getHealthyConnection 获取健康连接
// 功能说明：
//
//	从连接池中获取指定键的健康连接
//
// 参数：
//
//	key - 连接键（格式：ip:port）
//
// 返回值：
//
//	*UDPConnWrapper - 连接包装器
//	bool - 是否成功获取
//
// 健康检查：
//
//	检查连接是否健康且有效
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

// getHealthyFileConn 获取健康文件连接
// 功能说明：
//
//	获取指定FdtID的第一个健康连接
//
// 参数：
//
//	fdtID - 文件数据传输标识符
//
// 返回值：
//
//	*UDPConnWrapper - 连接包装器
//	bool - 是否成功获取
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

// removeUnhealthyFileConn 移除不健康的文件连接
// 功能说明：
//
//	从文件连接列表中移除指定的不健康连接
//
// 参数：
//
//	fdtID         - 文件数据传输标识符
//	targetWrapper - 要移除的连接包装器
//
// 处理流程：
//  1. 从连接列表中过滤掉目标连接
//  2. 更新或删除文件连接映射
//  3. 关闭连接并更新统计
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

// createNewConnection 创建新连接
// 功能说明：
//
//	创建新的UDP连接并添加到连接池
//
// 参数：
//
//	key  - 连接键（格式：ip:port）
//	port - 目标端口
//
// 返回值：
//
//	*UDPConnWrapper - 新创建的连接包装器
//	error - 创建过程中的错误
//
// 创建策略：
//
//	根据连接池模式（发送/接收）选择创建方式
//	优化连接参数，设置缓冲区大小
func (g *GlobalConnectionPool) createNewConnection(key string, port int) (*UDPConnWrapper, error) {
	var wrapper *UDPConnWrapper
	if g.Mode == 0 { // 发送模式
		log.Printf("[Pool] 发送模式：创建连接到 %s", key)
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
		log.Printf("[Pool] 发送连接创建成功：%s", key)
	} else { // 接收模式
		log.Printf("[Pool] 接收模式：在 %s 上监听", key)
		Conn, err := utils.CreateUDPListener(key)
		if err != nil {
			log.Printf("[Pool] 监听失败：%s，错误：%v", key, err)
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
		log.Printf("[Pool] 接收监听创建成功：%s", key)
	}

	g.Connections.Store(key, wrapper)
	atomic.AddInt32(&stats.TotalConns, 1)
	atomic.AddInt32(&stats.CreatedConns, 1)
	atomic.AddInt32(&stats.ActiveConns, 1)
	stats.LastPort = max(stats.LastPort, int16(port))

	return wrapper, nil
}

// CreateNewFileConn 为文件创建多个连接
// 功能说明：
//
//	为指定文件传输创建多个连接
//
// 参数：
//
//	fdtID   - 文件数据传输标识符
//	numConn - 需要创建的连接数
//
// 返回值：
//
//	[]*UDPConnWrapper - 创建的连接列表
//	[]error           - 创建过程中遇到的错误
//
// 创建策略：
//  1. 尝试复用现有连接
//  2. 创建新连接
//  3. 将连接关联到FdtID
func (g *GlobalConnectionPool) CreateNewFileConn(fdtID uint8, numConn uint8) ([]*UDPConnWrapper, []error) {
	return g.CreateNewFileConnWithBasePort(fdtID, numConn, int(stats.LastPort)+1)
}

// CreateNewFileConnWithBasePort 指定起始端口创建文件连接
// 功能说明：
//
//	从指定起始端口开始创建多个文件连接
//
// 参数：
//
//	fdtID   - 文件数据传输标识符
//	numConn - 需要创建的连接数
//	basePort - 起始端口号
//
// 返回值：
//
//	[]*UDPConnWrapper - 创建的连接列表
//	[]error           - 创建过程中遇到的错误
//
// 端口分配：
//
//	从basePort开始连续分配端口
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

// optimizeConnection 优化连接参数
// 功能说明：
//
//	设置UDP连接的优化参数
//
// 参数：
//
//	Conn - UDP连接指针
//
// 优化项：
//  1. 设置读写缓冲区大小（128MB）
//  2. 清除读写超时，避免长时间运行过期
func (g *GlobalConnectionPool) optimizeConnection(Conn *net.UDPConn) {
	// 设置发送/接收缓冲区大小
	Conn.SetReadBuffer(128 * 1024 * 1024)  // 128MB
	Conn.SetWriteBuffer(128 * 1024 * 1024) // 128MB

	// 默认不设置固定超时，避免长时间运行后触发过期 deadline
	var zero time.Time
	Conn.SetReadDeadline(zero)
	Conn.SetWriteDeadline(zero)
}

// isConnectionValid 检查连接有效性
// 功能说明：
//
//	检查连接是否健康且可用
//
// 参数：
//
//	wrapper - 连接包装器
//
// 返回值：
//
//	bool - true表示连接有效，false表示无效
//
// 检查条件：
//  1. 连接最后使用时间不超过30秒
//  2. 连接健康状态为true
func (g *GlobalConnectionPool) isConnectionValid(wrapper *UDPConnWrapper) bool {
	// 检查连接是否超时（30秒未使用）
	if time.Since(time.Unix(atomic.LoadInt64(&wrapper.LastUsed), 0)) > 30*time.Second {
		return false
	}

	//TODO:添加更多健康检查逻辑
	return wrapper.IsHealthy
}

// healthCheck 健康检查协程
// 功能说明：
//
//	定期检查连接池中所有连接的健康状态
//
// 检查频率：
//
//	每分钟检查一次
//
// 清理策略：
//  1. 检查普通连接池
//  2. 检查文件连接池
//  3. 移除不健康的连接
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

// ReturnConnection 归还连接
// 功能说明：
//
//	更新连接的最后使用时间，减少活跃连接计数
//
// 参数：
//
//	wrapper - 连接包装器
//
// 使用场景：
//
//	连接使用完成后调用，更新连接状态
func (g *GlobalConnectionPool) ReturnConnection(wrapper *UDPConnWrapper) {
	atomic.StoreInt64(&wrapper.LastUsed, time.Now().Unix())
	atomic.AddInt32(&stats.ActiveConns, -1)
}

// CloseConnection 关闭指定连接
// 功能说明：
//
//	关闭并移除指定的连接
//
// 参数：
//
//	destIP - 目标IP地址
//	port   - 目标端口
//
// 关闭流程：
//  1. 从连接池中查找连接
//  2. 设置连接为不健康状态
//  3. 关闭网络连接
//  4. 从连接池中移除
//  5. 更新统计信息
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

// CloseMetaConn 关闭元数据连接
// 功能说明：
//
//	关闭元数据相关的所有连接
//
// 处理流程：
//  1. 获取元数据连接
//  2. 关闭网络连接
//  3. 关闭文件连接
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

// CloseFileConn 关闭文件相关连接
// 功能说明：
//
//	关闭指定FdtID的所有连接
//
// 参数：
//
//	fdtID - 文件数据传输标识符
//
// 关闭流程：
//  1. 查找指定FdtID的所有连接
//  2. 设置连接为不健康状态
//  3. 关闭所有网络连接
//  4. 从连接池中移除
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

// CloseAllFileConn 关闭所有文件连接
// 功能说明：
//
//	关闭连接池中所有的文件连接
//
// 实现方式：
//
//	遍历所有FdtID，逐个关闭文件连接
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

// GetStats 获取连接池统计信息
// 功能说明：
//
//	获取连接池的运行时统计信息
//
// 返回值：
//
//	PoolStats - 连接池统计信息结构
func (g *GlobalConnectionPool) GetStats() PoolStats {
	return PoolStats{
		TotalConns:     atomic.LoadInt32(&stats.TotalConns),
		ActiveConns:    atomic.LoadInt32(&stats.ActiveConns),
		CreatedConns:   atomic.LoadInt32(&stats.CreatedConns),
		DestroyedConns: atomic.LoadInt32(&stats.DestroyedConns),
		LastPort:       stats.LastPort,
	}
}

// ShowInfo 显示连接池信息
// 功能说明：
//
//	打印连接池的统计信息到控制台
//
// 输出内容：
//   - 总连接数
//   - 活跃连接数
//   - 已创建连接数
//   - 已销毁连接数
//   - 最后使用端口
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

// closeWrapperIdle 关闭空闲连接器
// 功能说明：
//
//	关闭指定的空闲连接，清理相关资源
//
// 参数：
//
//	wrapper - 要关闭的连接器
//
// 关闭流程：
//  1. 设置连接为不健康状态
//  2. 关闭网络连接
//  3. 从文件连接映射中移除
//  4. 从全局连接映射中移除
//  5. 更新统计信息
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

// idleSenderMonitor 空闲发送者监控
// 功能说明：
//
//	定期检查发送连接是否空闲，关闭长时间未使用的连接
//
// 监控策略：
//
//	每秒检查一次，关闭超过3秒未发送数据的连接
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

// Stop 停止连接池
// 功能说明：
//
//	停止连接池的所有监控协程
//
// 使用场景：
//
//	应用程序退出时调用，清理资源
func (g *GlobalConnectionPool) Stop() {
	close(g.stopChan)
}
