package pool

import (
	"FluteGo/constant"
	"FluteGo/pkg/sock"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	POOL_SEND = 0
	POOL_RECV = 1
)

type ConnPool struct {
	Mode         uint8
	FileConns    sync.Map
	Conns        sync.Map
	MaxConns     int
	ConnTimeout  time.Duration
	LastMetaRecv int64
	Received     uint64
	StopChan     chan struct{}
	DestIP       string
	FileChunks   sync.Map
	MulticastIP  string // 多播地址，为空时表示单播模式
	MulticastIfaceIP string // 多播出口接口的 IP 地址，为空时自动选择
}

type PoolStats struct {
	Mode           uint8
	TotalConns     int32
	ActiveConns    int32
	CreatedConns   int32
	DestoryedConns int32
	LastPort       int32
}

var (
	connPool *ConnPool
	poolOnce sync.Once
	stats    PoolStats
)

func InitConnPool(destIP string, mode uint8) {
	InitConnPoolWithMulticast(destIP, mode, "")
}

func InitConnPoolWithMulticast(destIP string, mode uint8, multicastIP string) {
	poolOnce.Do(func() {
		ifaceIPLock.Lock()
		ifaceIP := globalMulticastIfaceIP
		ifaceIPLock.Unlock()

		connPool = &ConnPool{
			Mode:             mode,
			MaxConns:         100,
			ConnTimeout:      -1,
			DestIP:           destIP,
			StopChan:         make(chan struct{}),
			MulticastIP:      multicastIP,
			MulticastIfaceIP: ifaceIP,
		}
		stats.LastPort = 0 // 统一端口架构，不再使用 meta port
		go connPool.healthCheck()
		go connPool.idleSenderMonitor()
	})
}

func GetConnPool() *ConnPool {
	return connPool
}

func (p *ConnPool) createNewConn(ip string, port int) (*sock.MsSocket, error) {
	var msck *sock.MsSocket
	var err error

	if p.Mode == POOL_SEND {
		// Sender binds to random port, but uses provided ip and port as target
		msck, err = sock.CreateMsSocket(ip, port, sock.ModeSend)
	} else {
		// Receiver binds to all local interfaces (0.0.0.0) to receive from any IP
		msck, err = sock.CreateMsSocket("0.0.0.0", port, sock.ModeRecv)
	}

	if err != nil {
		return nil, err
	}

	// 设置接收缓冲区，防止高吞吐下丢包
	// 尝试设置较大的缓冲区，如果失败则逐步降级
	bufferSizes := []int{64 * 1024 * 1024, 32 * 1024 * 1024, 16 * 1024 * 1024, 8 * 1024 * 1024, 4 * 1024 * 1024}
	setSuccess := false
	for _, bufSize := range bufferSizes {
		if err := msck.Socket.SetReadBuffer(bufSize); err == nil {
			setSuccess = true
			break
		}
	}
	if !setSuccess {
		// 最后尝试系统默认值（不设置）
	}

	if p.Mode == POOL_SEND {
		msck.Socket.Shutdown(0)
		// 尝试设置发送缓冲区，如果失败则逐步降级
		if err := msck.Socket.SetWriteBuffer(constant.TX_BUF); err != nil {
			// 尝试 4MB
			if err := msck.Socket.SetWriteBuffer(4 * 1024 * 1024); err != nil {
				// 尝试 2MB
				if err := msck.Socket.SetWriteBuffer(2 * 1024 * 1024); err != nil {
					// 尝试 1MB
					msck.Socket.SetWriteBuffer(1 * 1024 * 1024)
				}
			}
		}
		// 多播模式：设置 TTL 和出口接口（默认 TTL=1 只能本机回环，跨设备需要更大值）
		if p.MulticastIP != "" {
			mcastIP := net.ParseIP(p.MulticastIP)
			if mcastIP != nil && mcastIP.To4() != nil {
				// 设置多播 TTL
				if err := msck.Socket.SetMulticastTTL(constant.MulticastTTL); err != nil {
					log.Printf("[multicast] set TTL %d failed: %v", constant.MulticastTTL, err)
				} else {
					log.Printf("[multicast] sender TTL set to %d", constant.MulticastTTL)
				}
				// 查找指定的多播出口接口（如果设置了 MulticastIfaceIP）
				var iface *net.Interface
				if p.MulticastIfaceIP != "" {
					ifaceIP := net.ParseIP(p.MulticastIfaceIP)
					if ifaceIP != nil {
						if foundIf, err := findInterfaceByIP(ifaceIP); err == nil {
							iface = foundIf
							log.Printf("[multicast] using specified interface: %s (%s)", iface.Name, p.MulticastIfaceIP)
						} else {
							log.Printf("[multicast] interface with IP %s not found: %v, using auto-select", p.MulticastIfaceIP, err)
						}
					}
				}
				// 加入多播组以自动设置出口接口（JoinMulticastGroup 会设置 IP_MULTICAST_IF）
				if err := msck.Socket.JoinMulticastGroup(mcastIP.To4(), iface); err != nil {
					log.Printf("[multicast] sender join group %s failed: %v", p.MulticastIP, err)
				} else {
					log.Printf("[multicast] sender joined group %s", p.MulticastIP)
				}
			}
		}
	}
	if p.Mode == POOL_RECV {
		msck.Socket.Shutdown(1)
		// 尝试设置接收缓冲区，如果失败则逐步降级
		if err := msck.Socket.SetReadBuffer(constant.RX_BUF); err != nil {
			// 尝试 8MB
			if err := msck.Socket.SetReadBuffer(8 * 1024 * 1024); err != nil {
				// 尝试 4MB
				if err := msck.Socket.SetReadBuffer(4 * 1024 * 1024); err != nil {
					// 尝试 2MB
				// 最后尝试，不检查错误
				msck.Socket.SetReadBuffer(1024 * 1024) // 1MB 兜底
				}
			}
		}

		// 如果配置了多播地址，加入多播组
		if p.MulticastIP != "" {
			mcastIP := net.ParseIP(p.MulticastIP)
			if mcastIP != nil && mcastIP.To4() != nil {
				// 查找指定的多播接口（如果设置了 MulticastIfaceIP）
				var iface *net.Interface
				if p.MulticastIfaceIP != "" {
					ifaceIP := net.ParseIP(p.MulticastIfaceIP)
					if ifaceIP != nil {
						if foundIf, err := findInterfaceByIP(ifaceIP); err == nil {
							iface = foundIf
							log.Printf("[multicast] receiver using specified interface: %s (%s)", iface.Name, p.MulticastIfaceIP)
						} else {
							log.Printf("[multicast] interface with IP %s not found: %v, using auto-select", p.MulticastIfaceIP, err)
						}
					}
				}
				if err := msck.Socket.JoinMulticastGroup(mcastIP.To4(), iface); err != nil {
					log.Printf("[multicast] join group %s on port %d failed: %v", p.MulticastIP, port, err)
				} else {
					log.Printf("[multicast] joined group %s on port %d", p.MulticastIP, port)
				}
			}
		}
	}
	// 输出实际缓冲区大小
	if fd := msck.Socket.Socket(); fd > 0 {
		rcvBuf, sndBuf := getSocketBufferSize(fd)
		log.Printf("[buffer] %s:%d rcvbuf=%d (%.1fMB) sndbuf=%d (%.1fMB)", ip, port, rcvBuf, float64(rcvBuf)/1024/1024, sndBuf, float64(sndBuf)/1024/1024)
	}

	flags := uint32(0)

	msck.IsHealthy = true
	msck.Flags = flags
	msck.LastUsed = time.Now().Unix()

	addrKey := fmt.Sprintf("%s:%d", ip, port)
	p.Conns.Store(addrKey, msck)

	atomic.AddInt32(&stats.TotalConns, 1)
	atomic.AddInt32(&stats.ActiveConns, 1)
	atomic.AddInt32(&stats.CreatedConns, 1)

	return msck, nil
}

func (p *ConnPool) AddReceived(n uint64) {
	if n == 0 {
		return
	}
	atomic.AddUint64(&p.Received, n)
}

func (p *ConnPool) ReceivedBytes() uint64 {
	return atomic.LoadUint64(&p.Received)
}

type chunkProgress struct {
	expected uint32
	written  uint32
}

func (p *ConnPool) SetChunkTarget(fdtID uint8, target uint32) {
	if fdtID == 0 || target == 0 {
		return
	}

	if _, ok := p.FileChunks.Load(fdtID); ok {
		return
	}

	p.FileChunks.Store(fdtID, &chunkProgress{
		expected: target,
		written:  0,
	})
}

func (p *ConnPool) MarkChunkWritten(fdtID uint8) uint32 {
	value, ok := p.FileChunks.Load(fdtID)
	if !ok {
		return 0
	}
	cp := value.(*chunkProgress)
	written := atomic.AddUint32(&cp.written, 1)
	return written
}

func (p *ConnPool) ChunkTargetReached(fdtID uint8) bool {
	value, ok := p.FileChunks.Load(fdtID)
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

// findInterfaceByIP 根据 IP 地址查找对应的网络接口
func findInterfaceByIP(ip net.IP) (*net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				if ipnet.IP.Equal(ip) {
					return &ifc, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("no interface found with IP %s", ip.String())
}

// SetMulticastIfaceIP 设置多播出口接口 IP（全局，必须在 InitConnPool 之前调用）
func SetMulticastIfaceIP(ip string) {
	ifaceIPLock.Lock()
	defer ifaceIPLock.Unlock()
	globalMulticastIfaceIP = ip
}

var (
	ifaceIPLock            sync.Mutex
	globalMulticastIfaceIP string
)

// InitMetaConn 已废弃，统一端口架构不再使用独立的 meta port
func (p *ConnPool) InitMetaConn() (*sock.MsSocket, error) {
	return nil, fmt.Errorf("InitMetaConn is deprecated, use unified port architecture")
}

func (p *ConnPool) CreateFileConn(fdtID uint8, numConn uint8, basePort int) ([]*sock.MsSocket, []error) {
	log.Printf("[pool] CreateFileConn: fdtID=%d, numConn=%d, basePort=%d, multicast=%s", fdtID, numConn, basePort, p.MulticastIP)
	var conns []*sock.MsSocket
	var errs []error

	for i := 0; i < int(numConn); i++ {
		port := basePort + i
		log.Printf("[pool] creating connection for port %d", port)
		msck, err := p.createNewConn(p.DestIP, port)
		if err != nil {
			log.Printf("[pool] failed to create connection for port %d: %v", port, err)
			errs = append(errs, err)
			continue
		}
		msck.FdtID = fdtID
		conns = append(conns, msck)
	}

	if len(conns) > 0 {
		p.FileConns.Store(fdtID, conns)
	}

	return conns, errs
}

func (p *ConnPool) isHealthyConn(msck *sock.MsSocket) bool {
	// Meta connection (FdtID 0) should not timeout
	if msck.FdtID == 0 {
		return true
	}

	// 获取最后活动时间（取LastUsed和LastSent的较晚者）
	lastUsed := atomic.LoadInt64(&msck.LastUsed)
	lastSentNano := atomic.LoadInt64(&msck.LastSent)

	lastAct := time.Unix(lastUsed, 0)
	if lastSentNano > 0 {
		lastSent := time.Unix(0, lastSentNano)
		if lastSent.After(lastAct) {
			lastAct = lastSent
		}
	}

	if time.Since(lastAct) > constant.CONN_TIMEOUT*time.Second {
		// log.Printf("Connection timeout: FdtID=%d, LastAct=%s, Timeout=%d", wsck.FdtID, lastAct.Format(time.RFC3339), constant.CONN_TIMEOUT)
		// wsck.IsHealthy = false
	}

	return msck.IsHealthy
}

func (p *ConnPool) healthCheck() {
	ticker := time.NewTicker(constant.HEALTH_CHECK_INTERVAL * time.Second) // 每10s检查一次
	defer ticker.Stop()

	for {
		select {
		case <-p.StopChan:
			return
		case <-ticker.C:
			p.Conns.Range(func(key, value interface{}) bool {
				msck := value.(*sock.MsSocket)
				if !p.isHealthyConn(msck) {
					p.Conns.Delete(key)
					msck.Socket.Close()
				}
				return true
			})

			p.FileConns.Range(func(key, value interface{}) bool {
				msck := value.([]*sock.MsSocket)
				var healthyMsck []*sock.MsSocket
				for _, w := range msck {
					if p.isHealthyConn(w) {
						healthyMsck = append(healthyMsck, w)
					} else {
						w.Mu.Lock()
						w.Socket.Close()
						w.Mu.Unlock()
						atomic.AddInt32(&stats.TotalConns, -1)
						atomic.AddInt32(&stats.DestoryedConns, 1)
						atomic.AddInt32(&stats.ActiveConns, -1)
					}
				}
				if len(healthyMsck) == 0 {
					p.FileConns.Delete(key)
				} else {
					p.FileConns.Store(key, healthyMsck)
				}
				return true
			})
		}
	}
}

func (p *ConnPool) closeConn(ip string, port int) {
	addr := fmt.Sprintf("%s:%d", ip, port)
	if val, ok := p.Conns.Load(addr); ok {
		msck := val.(*sock.MsSocket)
		msck.Mu.RLock()
		if !msck.IsHealthy {
			msck.Mu.RUnlock()
			return
		}
		msck.Mu.RUnlock()

		msck.Mu.Lock()
		defer msck.Mu.Unlock()
		if msck.IsHealthy {
			msck.IsHealthy = false
			msck.Socket.Close()
			p.Conns.Delete(addr)
			atomic.AddInt32(&stats.TotalConns, -1)
			atomic.AddInt32(&stats.DestoryedConns, 1)
			if stats.ActiveConns > 0 {
				atomic.AddInt32(&stats.ActiveConns, -1)
			}
		}
	}
}

func (p *ConnPool) Get(ip string, port int) (*sock.MsSocket, error) {
	addr := fmt.Sprintf("%s:%d", ip, port)
	if val, ok := p.Conns.Load(addr); ok {
		msck := val.(*sock.MsSocket)
		if p.isHealthyConn(msck) {
			return msck, nil
		}
		p.closeConn(ip, port)
	}
	return p.createNewConn(ip, port)
}

func (p *ConnPool) GetFileConn(fdtID uint8) (uint16, []*sock.MsSocket, error) {
	if val, ok := p.FileConns.Load(fdtID); ok {
		mscks := val.([]*sock.MsSocket)
		var healthyMscks []*sock.MsSocket
		for _, w := range mscks {
			if p.isHealthyConn(w) {
				w.MarkSent() // Mark as active to prevent idle timeout
				healthyMscks = append(healthyMscks, w)
			} else {
				w.Mu.Lock()
				defer w.Mu.Unlock()
				w.Socket.Close()
				atomic.AddInt32(&stats.TotalConns, -1)
				atomic.AddInt32(&stats.DestoryedConns, 1)
				atomic.AddInt32(&stats.ActiveConns, -1)
			}
		}
		if len(healthyMscks) == 0 {
			p.FileConns.Delete(fdtID)
		} else {
			p.FileConns.Store(fdtID, healthyMscks)
		}
		return uint16(len(healthyMscks)), healthyMscks, nil
	}
	return 0, nil, fmt.Errorf("file connections not found for fdtID %d", fdtID)
}

func (p *ConnPool) GetMetaConn() (*sock.MsSocket, error) {
	_, conns, err := p.GetFileConn(0)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
		return nil, fmt.Errorf("no meta connections available")
	}
	return conns[0], nil
}

func (p *ConnPool) CloseMetaConn() error {
	metaConn, err := p.GetMetaConn()
	if err != nil || metaConn == nil || metaConn.Socket == nil {
		if err != nil {
			p.CloseFileConn(0)
			return fmt.Errorf("CloseMetaConn skipped: %v\n", err)
		} else {
			return fmt.Errorf("CloseMetaConn skipped: meta connection unavailable\n")
		}
	}

	p.CloseFileConn(0)
	return nil
}

func (p *ConnPool) CloseFileConn(fdtID uint8) error {
	if val, ok := p.FileConns.Load(fdtID); ok {
		mscks := val.([]*sock.MsSocket)
		for _, w := range mscks {
			w.Mu.Lock()
			if w.IsHealthy {
				// log.Printf("Closing file connection: FdtID=%d", fdtID)
				w.IsHealthy = false
				w.Socket.Close()
				atomic.AddInt32(&stats.TotalConns, -1)
				atomic.AddInt32(&stats.DestoryedConns, 1)
				if stats.ActiveConns > 0 {
					atomic.AddInt32(&stats.ActiveConns, -1)
				}
			}
			w.Mu.Unlock()

			// Remove from Conns map to prevent handle reuse issues
			if w.Addr != nil {
				key := fmt.Sprintf("%s:%d", w.Addr.IP.String(), w.Addr.Port)
				p.Conns.Delete(key)
			}
		}
		p.FileConns.Delete(fdtID)
	}
	return nil
}

func (p *ConnPool) CloseAllConns() {
	var fdtIDs []uint8
	p.FileConns.Range(func(key, value interface{}) bool {
		fdtID := key.(uint8)
		fdtIDs = append(fdtIDs, fdtID)
		return true
	})

	for _, fdtID := range fdtIDs {
		p.CloseFileConn(fdtID)
	}
}

func (p *ConnPool) closeIdle(wsck *sock.MsSocket) {
	wsck.Mu.Lock()
	defer wsck.Mu.Unlock()
	if wsck.IsHealthy {
		// log.Printf("Closing idle connection: FdtID=%d", wsck.FdtID)
		wsck.IsHealthy = false
		wsck.Socket.Close()
		atomic.AddInt32(&stats.TotalConns, -1)
		atomic.AddInt32(&stats.DestoryedConns, 1)
		if stats.ActiveConns > 0 {
			atomic.AddInt32(&stats.ActiveConns, -1)
		}
	}
}

func (p *ConnPool) idleSenderMonitor() {
	// 如果是接收端模式，不需要监控发送空闲，因为接收端主要任务是接收
	// 否则会导致接收端连接在 60s 后被误杀
	if p.Mode == constant.POOL_RECV {
		return
	}

	ticker := time.NewTicker(constant.IDLE_SENDER_CHECK_INTERVAL * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.StopChan:
			return
		case <-ticker.C:
			p.Conns.Range(func(key, value interface{}) bool {
				wsck := value.(*sock.MsSocket)
				// Skip Meta connection
				if wsck.FdtID == 0 {
					return true
				}
				if atomic.LoadUint32(&wsck.SentData) == 0 {
					return true
				}
				last := atomic.LoadInt64(&wsck.LastSent)
				if last == 0 {
					return true
				}
				if time.Since(time.Unix(0, last)) > constant.IDLE_SENDER_TIMEOUT*time.Second {
					p.closeIdle(wsck)
				}
				return true
			})
		}
	}
}

func (p *ConnPool) GetStats() PoolStats {
	return PoolStats{
		Mode:           p.Mode,
		TotalConns:     atomic.LoadInt32(&stats.TotalConns),
		ActiveConns:    atomic.LoadInt32(&stats.ActiveConns),
		CreatedConns:   atomic.LoadInt32(&stats.CreatedConns),
		DestoryedConns: atomic.LoadInt32(&stats.DestoryedConns),
		LastPort:       atomic.LoadInt32(&stats.LastPort),
	}
}

func (p *ConnPool) ShowInfo() {
	s := p.GetStats()
	fmt.Printf("TotalConns: %d\n", s.TotalConns)
	fmt.Printf("ActiveConns: %d\n", s.ActiveConns)
	fmt.Printf("CreatedConns: %d\n", s.CreatedConns)
	fmt.Printf("DestoryedConns: %d\n", s.DestoryedConns)
	fmt.Printf("LastPort: %d\n", s.LastPort)
}

func (p *ConnPool) Stop() {
	close(p.StopChan)
}
