//go:build linux || darwin || freebsd

package io

import (
	"FluteGo/pkg/sock"
	"sync"
)

type UnixIOHandler struct {
	msck          *sock.MsSocket
	maxPacketSize int
	dataQueue     chan []byte
	bufPool       *sync.Pool // 复用 MAX_PACKET_SIZE 的 buffer，消除二次拷贝
}

func init() {
	ioHandler = newUnixIOHandler
}

func (h *UnixIOHandler) TryDequeue() (interface{}, bool) {
	data, ok := <-h.dataQueue
	return data, ok
}

func newUnixIOHandler(msck *sock.MsSocket, maxPacketSize int) (IOHandler, error) {
	pool := &sync.Pool{
		New: func() interface{} {
			b := make([]byte, maxPacketSize)
			return b
		},
	}
	return &UnixIOHandler{
		msck:          msck,
		maxPacketSize: maxPacketSize,
		dataQueue:     make(chan []byte, 16384),
		bufPool:       pool,
	}, nil
}

func (h *UnixIOHandler) Start() {
	// 启动接收协程
	go func() {
		for {
			// 从池获取缓冲区，避免每包分配
			buf := h.bufPool.Get().([]byte)

			n, err := h.msck.Socket.ReadFromUDP(buf)
			if err != nil {
				h.bufPool.Put(buf)
				return
			}

			// 直接传递 pool buffer 切片 —— 无二次拷贝
			// 消费者必须同步处理或拷贝数据后归还到 bufPool
			h.dataQueue <- buf[:n]
		}
	}()
}

func (h *UnixIOHandler) Stop() {
	close(h.dataQueue)
}

func (h *UnixIOHandler) GetDataQueue() chan []byte {
	return h.dataQueue
}

func (h *UnixIOHandler) ReturnContext(ctx interface{}) {
	// Unix 实现：如果 ctx 是 []byte，归还到 bufPool
	if buf, ok := ctx.([]byte); ok {
		h.bufPool.Put(buf[:cap(buf)])
	}
}
