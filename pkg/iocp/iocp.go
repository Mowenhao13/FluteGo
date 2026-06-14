//go:build windows
/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package iocp

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/puzpuzpuz/xsync/v3"
	"golang.org/x/sys/windows"
)

// PacketHandler 定义处理接收到的数据包的回调函数
// 注意：data 切片底层的内存是复用的，如果需要异步处理，必须进行拷贝
type PacketHandler func(data []byte)

// IOContext 每个异步 I/O 操作的上下文
// 必须保持内存地址固定，不能被 GC 移动（通过切片引用保持）
type IOContext struct {
	Overlapped windows.Overlapped
	Buffer     windows.WSABuf
	Data       []byte                 // 实际的数据缓冲区
	From       windows.RawSockaddrAny // 发送端地址
	FromLen    int32
	Flags      uint32
	BytesRecv  uint32
	server     *IOCPServer // 反向引用，用于 Repost
}

// IOCPServer 管理 IOCP 端口和 Worker 线程
type IOCPServer struct {
	handle      windows.Handle
	socket      windows.Handle
	contexts    []*IOContext // 保持所有 Context 的引用，防止 GC
	dataQueue   *xsync.MPMCQueueOf[*IOContext]
	bufferSize  int
	workerCount int
	isRunning   int32
	wg          sync.WaitGroup
}

// NewIOCPServer 创建一个新的 IOCP 服务器实例
// socket: 已经绑定端口的 UDP Socket 句柄
// bufSize: 单个数据包的最大缓冲区大小 (建议 2048 或更大)
// concurrency: 并发 Worker 数量 (0 表示使用 CPU 核心数)
// queueSize: 数据队列大小 (建议 4096 或更大)
func NewIOCPServer(socket windows.Handle, bufSize int, concurrency int, queueSize int) (*IOCPServer, error) {
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}
	if queueSize <= 0 {
		queueSize = 4096
	}

	// 1. 创建 IOCP 端口
	// FileHandle=InvalidHandle 表示创建一个新的 IOCP
	// NumberOfConcurrentThreads=0 表示允许尽可能多的线程运行
	iocpHandle, err := windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("CreateIoCompletionPort failed: %v", err)
	}

	// 2. 将 Socket 关联到 IOCP
	if _, err := windows.CreateIoCompletionPort(socket, iocpHandle, 0, 0); err != nil {
		windows.CloseHandle(iocpHandle)
		return nil, fmt.Errorf("Associating socket with IOCP failed: %v", err)
	}

	return &IOCPServer{
		handle:      iocpHandle,
		socket:      socket,
		bufferSize:  bufSize,
		workerCount: concurrency,
		dataQueue:   xsync.NewMPMCQueueOf[*IOContext](queueSize),
		contexts:    make([]*IOContext, 0),
	}, nil
}

// PostReceives 预投递接收请求
// count: 预投递的数量 (建议 1024 - 4096)
func (s *IOCPServer) PostReceives(count int) error {
	for i := 0; i < count; i++ {
		// 分配内存
		data := make([]byte, s.bufferSize)
		ctx := &IOContext{
			Data:   data,
			server: s,
		}
		// 初始化 WSABuf
		ctx.Buffer.Len = uint32(len(data))
		ctx.Buffer.Buf = &ctx.Data[0]
		ctx.FromLen = int32(unsafe.Sizeof(ctx.From))

		// 保存引用
		s.contexts = append(s.contexts, ctx)

		// 首次投递
		if err := s.postRecv(ctx); err != nil {
			return err
		}
	}
	return nil
}

// postRecv 投递单个接收请求
func (s *IOCPServer) postRecv(ctx *IOContext) error {
	// 重置状态
	ctx.Flags = 0
	ctx.FromLen = int32(unsafe.Sizeof(ctx.From))
	// 清空 Overlapped (重要)
	// Go 的 struct 内存布局中 Overlapped 在最前面，可以直接清零
	// 但为了安全，手动重置 Internal/InternalHigh/Offset/OffsetHigh/HEvent
	ctx.Overlapped.Internal = 0
	ctx.Overlapped.InternalHigh = 0
	ctx.Overlapped.Offset = 0
	ctx.Overlapped.OffsetHigh = 0
	ctx.Overlapped.HEvent = 0

	err := windows.WSARecvFrom(
		s.socket,
		&ctx.Buffer,
		1,
		&ctx.BytesRecv,
		&ctx.Flags,
		&ctx.From,
		&ctx.FromLen,
		&ctx.Overlapped,
		nil, // CompletionRoutine
	)

	if err != nil && err != windows.ERROR_IO_PENDING {
		return fmt.Errorf("WSARecvFrom failed: %w", err)
	}
	return nil
}

// Start 启动 Worker 线程处理完成事件
func (s *IOCPServer) Start() {
	atomic.StoreInt32(&s.isRunning, 1)
	for i := 0; i < s.workerCount; i++ {
		s.wg.Add(1)
		go s.workerLoop(i)
	}
	log.Printf("IOCP Server started with %d workers", s.workerCount)
}

// Stop 停止服务器
func (s *IOCPServer) Stop() {
	if atomic.CompareAndSwapInt32(&s.isRunning, 1, 0) {
		// 关闭 IOCP 句柄会唤醒所有 GetQueuedCompletionStatus 并返回错误
		windows.CloseHandle(s.handle)
		s.wg.Wait()
	}
}

// GetDataQueue 获取数据队列，供消费者使用
func (s *IOCPServer) GetDataQueue() *xsync.MPMCQueueOf[*IOContext] {
	return s.dataQueue
}

// ReturnContext 消费者处理完数据后，必须调用此方法归还 Context
func (s *IOCPServer) ReturnContext(ctx *IOContext) {
	if atomic.LoadInt32(&s.isRunning) == 0 {
		return
	}

	for {
		err := s.postRecv(ctx)
		if err == nil {
			return
		}

		// 如果 Socket 已经关闭 (WSAENOTSOCK) 或操作被取消 (WSAEINTR)，则不再报错并停止服务器
		if errors.Is(err, windows.WSAENOTSOCK) || errors.Is(err, windows.WSAEINTR) {
			// 避免重复调用 Stop
			if atomic.LoadInt32(&s.isRunning) == 1 {
				s.Stop()
			}
			return
		}

	// WSAECONNRESET in UDP means ICMP Port Unreachable. Retry with exponential backoff.
		// Must retry to avoid losing contexts and draining the receive pool.
		// Limited retries with backoff to prevent infinite loops under sustained errors.
		if errors.Is(err, windows.WSAECONNRESET) {
			const maxRetries = 10
			for i := 0; i < maxRetries; i++ {
				backoff := time.Duration(1<<i) * time.Microsecond
				if backoff > time.Millisecond {
					backoff = time.Millisecond
				}
				time.Sleep(backoff)
				if serr := s.postRecv(ctx); serr == nil {
					return
				}
			}
			log.Printf("Repost failed after %d WSAECONNRESET retries, dropping context", maxRetries)
			return
		}

		// 其他错误，记录日志并丢弃 Context (防止死循环)
		log.Printf("Repost failed: %v", err)
		return
	}
}

func (s *IOCPServer) workerLoop(id int) {
	defer s.wg.Done()

	var bytesTransferred uint32
	var key uintptr
	var overlapped *windows.Overlapped

	for {
		if atomic.LoadInt32(&s.isRunning) == 0 {
			return
		}

		// 阻塞等待 IOCP 事件
		err := windows.GetQueuedCompletionStatus(
			s.handle,
			&bytesTransferred,
			&key,
			&overlapped,
			windows.INFINITE,
		)

		if err != nil {
			// 如果服务器正在停止，忽略错误退出
			if atomic.LoadInt32(&s.isRunning) == 0 {
				return
			}
			// 超时或其他错误，继续
			// log.Printf("Worker %d GQCS error: %v", id, err)
			continue
		}

		if overlapped == nil {
			continue
		}

		// 通过 overlapped 指针找回 IOContext
		// IOContext 的第一个字段就是 Overlapped，所以地址相同
		ctx := (*IOContext)(unsafe.Pointer(overlapped))

		if bytesTransferred > 0 {
			// 将接收到的数据放入队列
			// 注意：此时 Context 仍被占用，直到消费者调用 ReturnContext
			ctx.BytesRecv = bytesTransferred
			s.dataQueue.Enqueue(ctx)
		} else {
			// 0 字节接收通常意味着错误或关闭，直接回收
			s.ReturnContext(ctx)
		}
	}
}
