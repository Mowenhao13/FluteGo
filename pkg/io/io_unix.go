//go:build linux || darwin || freebsd

package io

import (
	"FluteGo/pkg/sock"
)

type UnixIOHandler struct {
	msck          *sock.MsSocket
	maxPacketSize int
	dataQueue     chan []byte
}

func init() {
	ioHandler = newUnixIOHandler
}

func (h *UnixIOHandler) TryDequeue() (interface{}, bool) {
	data, ok := <-h.dataQueue
	return data, ok
}

func newUnixIOHandler(msck *sock.MsSocket, maxPacketSize int) (IOHandler, error) {
	return &UnixIOHandler{
		msck:          msck,
		maxPacketSize: maxPacketSize,
		dataQueue:     make(chan []byte, 16384),
	}, nil
}

func (h *UnixIOHandler) Start() {
	// 启动接收协程
	go func() {
		buf := make([]byte, h.maxPacketSize)
		for {
			n, err := h.msck.Socket.ReadFromUDP(buf)
			if err != nil {
				return
			}

			// 拷贝数据到新的缓冲区
			data := make([]byte, n)
			copy(data, buf[:n])

			// 发送到数据队列
			h.dataQueue <- data
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
	// Unix实现不需要此方法
}
