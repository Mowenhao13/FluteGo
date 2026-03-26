//go:build linux || darwin || freebsd

package iocp

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// PacketHandler 定义处理接收到的数据包的回调函数
// 在 Unix 实现中，data 是独立分配的缓冲区，可以安全地异步处理
type PacketHandler func(data []byte)

// IOContext Unix 实现的数据包上下文
type IOContext struct {
	Data      []byte
	BytesRecv uint32
}

// IOCPServer Unix 实现使用简单的 UDP 接收循环
// 注意：这是为了兼容 Windows IOCP 接口的伪实现
type IOCPServer struct {
	socket      int
	contexts    []*IOContext
	dataQueue   chan *IOContext
	bufferSize  int
	workerCount int
	isRunning   int32
	wg          sync.WaitGroup
	stopChan    chan struct{}
}

// NewIOCPServer 创建 Unix 实现（实际不使用 IOCP）
func NewIOCPServer(socketFd int, bufSize int, concurrency int, queueSize int) (*IOCPServer, error) {
	if concurrency <= 0 {
		concurrency = 1
	}
	if queueSize <= 0 {
		queueSize = 4096
	}

	return &IOCPServer{
		socket:      socketFd,
		bufferSize:  bufSize,
		workerCount: concurrency,
		dataQueue:   make(chan *IOContext, queueSize),
		contexts:    make([]*IOContext, 0),
		stopChan:    make(chan struct{}),
	}, nil
}

// PostReceives Unix 实现为空操作
func (s *IOCPServer) PostReceives(count int) error {
	return nil
}

// Start 启动接收循环
func (s *IOCPServer) Start() {
	atomic.StoreInt32(&s.isRunning, 1)
	s.wg.Add(s.workerCount)
	for i := 0; i < s.workerCount; i++ {
		go s.workerLoop(i)
	}
}

// Stop 停止服务器
func (s *IOCPServer) Stop() {
	if atomic.CompareAndSwapInt32(&s.isRunning, 1, 0) {
		close(s.stopChan)
		s.wg.Wait()
		// 关闭 socket
		if s.socket != 0 {
			unix.Close(s.socket)
		}
	}
}

// GetDataQueue 获取数据队列
func (s *IOCPServer) GetDataQueue() chan *IOContext {
	return s.dataQueue
}

// ReturnContext 归还 Context（Unix 实现直接丢弃）
func (s *IOCPServer) ReturnContext(ctx *IOContext) {
	// Unix 实现不需要归还，数据已经拷贝
}

// workerLoop 从 socket 读取数据
func (s *IOCPServer) workerLoop(id int) {
	defer s.wg.Done()

	buf := make([]byte, s.bufferSize)
	for {
		select {
		case <-s.stopChan:
			return
		default:
		}

		n, err := unix.Read(s.socket, buf)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				time.Sleep(time.Microsecond)
				continue
			}
			// Socket 关闭或其他错误
			return
		}

		if n > 0 {
			// 创建新的 IOContext
			ctx := &IOContext{
				Data:      make([]byte, n),
				BytesRecv: uint32(n),
			}
			copy(ctx.Data, buf[:n])

			select {
			case s.dataQueue <- ctx:
			case <-s.stopChan:
				return
			default:
				// 队列满，丢弃
			}
		}
	}
}
