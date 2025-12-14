/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */
/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package pool

import (
	"FluteGo/pkg/utils"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"FluteGo/constant"

	"unsafe"

	"golang.org/x/sys/windows"
)

type To struct {
	ToAny *windows.RawSockaddrAny
	ToLen int32
}

type From struct {
	FromAny *windows.RawSockaddrAny
	FromLen int32
}

type WinSocket struct {
	Addr      *net.UDPAddr
	Socket    windows.Handle
	LastUsed  int64
	LastSent  int64
	IsHealthy bool
	FdtID     uint8
	Mu        sync.RWMutex
	SentData  uint32
	Flags     uint32
	To
	From
}

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
	poolOnce.Do(func() {
		connPool = &ConnPool{
			Mode:        mode,
			MaxConns:    100,
			ConnTimeout: -1,
			DestIP:      destIP,
			StopChan:    make(chan struct{}),
		}
		stats.LastPort = constant.META_PORT
		go connPool.healthCheck()
		go connPool.idleSenderMonitor()
	})
}

func GetConnPool() *ConnPool {
	return connPool
}

func (p *ConnPool) createNewConn(ip string, port int) (*WinSocket, error) {
	var sck windows.Handle
	var err error

	if p.Mode == constant.POOL_SEND {
		// Sender binds to random port
		sck, err = utils.CreateSocket("0.0.0.0", 0)
	} else {
		// Receiver binds to listening port
		sck, err = utils.CreateSocket(ip, port)
	}

	if err != nil {
		return nil, err
	}

	// 设置接收缓冲区为 100MB，防止高吞吐下丢包
	// 2Gbps = 250MB/s，100MB 可以缓冲约 400ms 的数据
	const RCVBUF_SIZE = 100 * 1024 * 1024
	if err := windows.SetsockoptInt(sck, windows.SOL_SOCKET, windows.SO_RCVBUF, RCVBUF_SIZE); err != nil {
		// 如果设置失败，尝试设置一个较小的值 (e.g. 10MB)
		windows.SetsockoptInt(sck, windows.SOL_SOCKET, windows.SO_RCVBUF, 10*1024*1024)
	}

	if p.Mode == constant.POOL_SEND {
		windows.Shutdown(sck, windows.SHUT_RD)
	}
	if p.Mode == constant.POOL_RECV {
		windows.Shutdown(sck, windows.SHUT_WR)
	}

	nPort := uint16(port)
	ipAddr := net.ParseIP(ip).To4()
	addr := &net.UDPAddr{
		IP:   ipAddr,
		Port: int(nPort),
	}

	sendTo := To{
		ToAny: &windows.RawSockaddrAny{},
		ToLen: 0,
	}
	recvFrom := From{
		FromAny: &windows.RawSockaddrAny{},
		FromLen: 0,
	}
	flags := uint32(0)

	if p.Mode == constant.POOL_SEND {
		to := &windows.RawSockaddrInet4{
			Family: windows.AF_INET,
			Port:   (nPort<<8)&0xff00 | (nPort>>8)&0x00ff,
		}
		copy(to.Addr[:], ipAddr)

		toAny := (*windows.RawSockaddrAny)(unsafe.Pointer(to))
		toLen := int32(unsafe.Sizeof(*to))

		// const MSG_DONTWAIT = 0x40 // send mode

		// if p.Mode == constant.POOL_SEND {
		// 	flags |= MSG_DONTWAIT
		// }
		sendTo.ToAny = toAny
		sendTo.ToLen = toLen
	}

	// For POOL_RECV, we use the default initialized recvFrom (RawSockaddrAny)
	// which is large enough to hold any address.
	// We don't need to initialize it with local address.

	wsck := &WinSocket{
		Addr:      addr,
		Socket:    sck,
		LastUsed:  time.Now().Unix(),
		IsHealthy: true,
		Flags:     flags,
		To:        sendTo,
		From:      recvFrom,
	}

	addrKey := fmt.Sprintf("%s:%d", ip, port)
	p.Conns.Store(addrKey, wsck)

	atomic.AddInt32(&stats.TotalConns, 1)
	atomic.AddInt32(&stats.ActiveConns, 1)
	atomic.AddInt32(&stats.CreatedConns, 1)

	return wsck, nil
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

func (w *WinSocket) MarkSent() {
	atomic.StoreInt64(&w.LastSent, time.Now().UnixNano())
	atomic.StoreUint32(&w.SentData, 1)
}

func (w *WinSocket) HadSent() bool {
	return atomic.LoadUint32(&w.SentData) == 1
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

func (p *ConnPool) InitMetaConn() (*WinSocket, error) {
	conns, err := p.CreateFileConn(0, 1, constant.META_PORT)
	if err != nil || len(conns) == 0 {
		return nil, fmt.Errorf("failed to create meta connection: %v", err)
	}
	return conns[0], nil
}

func (p *ConnPool) CreateFileConn(fdtID uint8, numConn uint8, basePort int) ([]*WinSocket, []error) {
	var conns []*WinSocket
	var errs []error

	for i := 0; i < int(numConn); i++ {
		port := basePort + i
		wsck, err := p.createNewConn(p.DestIP, port)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		wsck.FdtID = fdtID
		conns = append(conns, wsck)
	}

	if len(conns) > 0 {
		p.FileConns.Store(fdtID, conns)
	}

	return conns, errs
}

func (p *ConnPool) isHealthyConn(wsck *WinSocket) bool {
	// Meta connection (FdtID 0) should not timeout
	if wsck.FdtID == 0 {
		return true
	}

	// 获取最后活动时间（取LastUsed和LastSent的较晚者）
	lastUsed := atomic.LoadInt64(&wsck.LastUsed)
	lastSentNano := atomic.LoadInt64(&wsck.LastSent)

	lastAct := time.Unix(lastUsed, 0)
	if lastSentNano > 0 {
		lastSent := time.Unix(0, lastSentNano)
		if lastSent.After(lastAct) {
			lastAct = lastSent
		}
	}

	if time.Since(lastAct) > constant.CONN_TIMEOUT*time.Second {
		// log.Printf("Connection timeout: FdtID=%d, LastAct=%s, Timeout=%d", wsck.FdtID, lastAct.Format(time.RFC3339), constant.CONN_TIMEOUT)
		wsck.IsHealthy = false
	}

	return wsck.IsHealthy
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
				wsck := value.(*WinSocket)
				if !p.isHealthyConn(wsck) {
					p.Conns.Delete(key)
					windows.Closesocket(wsck.Socket)
				}
				return true
			})

			p.FileConns.Range(func(key, value interface{}) bool {
				wsck := value.([]*WinSocket)
				var healthyWsck []*WinSocket
				for _, w := range wsck {
					if p.isHealthyConn(w) {
						healthyWsck = append(healthyWsck, w)
					} else {
						w.Mu.Lock()
						windows.Closesocket(w.Socket)
						w.Mu.Unlock()
						atomic.AddInt32(&stats.TotalConns, -1)
						atomic.AddInt32(&stats.DestoryedConns, 1)
						atomic.AddInt32(&stats.ActiveConns, -1)
					}
				}
				if len(healthyWsck) == 0 {
					p.FileConns.Delete(key)
				} else {
					p.FileConns.Store(key, healthyWsck)
				}
				return true
			})
		}
	}
}

func (p *ConnPool) closeConn(ip string, port int) {
	addr := fmt.Sprintf("%s:%d", ip, port)
	if val, ok := p.Conns.Load(addr); ok {
		wsck := val.(*WinSocket)
		wsck.Mu.RLock()
		if !wsck.IsHealthy {
			wsck.Mu.RUnlock()
			return
		}
		wsck.Mu.RUnlock()

		wsck.Mu.Lock()
		defer wsck.Mu.Unlock()
		if wsck.IsHealthy {
			wsck.IsHealthy = false
			windows.Closesocket(wsck.Socket)
			p.Conns.Delete(addr)
			atomic.AddInt32(&stats.TotalConns, -1)
			atomic.AddInt32(&stats.DestoryedConns, 1)
			if stats.ActiveConns > 0 {
				atomic.AddInt32(&stats.ActiveConns, -1)
			}
		}
	}
}

func (p *ConnPool) Get(ip string, port int) (*WinSocket, error) {
	addr := fmt.Sprintf("%s:%d", ip, port)
	if val, ok := p.Conns.Load(addr); ok {
		wsck := val.(*WinSocket)
		if p.isHealthyConn(wsck) {
			return wsck, nil
		}
		p.closeConn(ip, port)
	}
	return p.createNewConn(ip, port)
}

func (p *ConnPool) GetFileConn(fdtID uint8) (uint16, []*WinSocket, error) {
	if val, ok := p.FileConns.Load(fdtID); ok {
		wscks := val.([]*WinSocket)
		var healthyWscks []*WinSocket
		for _, w := range wscks {
			if p.isHealthyConn(w) {
				w.MarkSent() // Mark as active to prevent idle timeout
				healthyWscks = append(healthyWscks, w)
			} else {
				w.Mu.Lock()
				defer w.Mu.Unlock()
				windows.Closesocket(w.Socket)
				atomic.AddInt32(&stats.TotalConns, -1)
				atomic.AddInt32(&stats.DestoryedConns, 1)
				atomic.AddInt32(&stats.ActiveConns, -1)
			}
		}
		if len(healthyWscks) == 0 {
			p.FileConns.Delete(fdtID)
		} else {
			p.FileConns.Store(fdtID, healthyWscks)
		}
		return uint16(len(healthyWscks)), healthyWscks, nil
	}
	return 0, nil, fmt.Errorf("file connections not found for fdtID %d", fdtID)
}

func (p *ConnPool) GetMetaConn() (*WinSocket, error) {
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
	if err != nil || metaConn == nil || metaConn.Socket == 0 {
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
		wscks := val.([]*WinSocket)
		for _, w := range wscks {
			w.Mu.Lock()
			if w.IsHealthy {
				// log.Printf("Closing file connection: FdtID=%d", fdtID)
				w.IsHealthy = false
				windows.Closesocket(w.Socket)
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

func (p *ConnPool) closeIdle(wsck *WinSocket) {
	wsck.Mu.Lock()
	defer wsck.Mu.Unlock()
	if wsck.IsHealthy {
		// log.Printf("Closing idle connection: FdtID=%d", wsck.FdtID)
		wsck.IsHealthy = false
		windows.Closesocket(wsck.Socket)
		atomic.AddInt32(&stats.TotalConns, -1)
		atomic.AddInt32(&stats.DestoryedConns, 1)
		if stats.ActiveConns > 0 {
			atomic.AddInt32(&stats.ActiveConns, -1)
		}
	}
}

func (p *ConnPool) idleSenderMonitor() {
	ticker := time.NewTicker(constant.IDLE_SENDER_CHECK_INTERVAL * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.StopChan:
			return
		case <-ticker.C:
			p.Conns.Range(func(key, value interface{}) bool {
				wsck := value.(*WinSocket)
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
