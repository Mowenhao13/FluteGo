//go:build windows

package io

import (
	"FluteGo/pkg/iocp"
	"FluteGo/pkg/sock"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

type WinIOHandler struct {
	server   *iocp.IOCPServer
	dataCh   chan []byte
	stopCh   chan struct{}
	stopOnce sync.Once
}

func init() {
	ioHandler = newWinIOHandler
}

func newWinIOHandler(msck *sock.MsSocket, maxPacketSize int) (IOHandler, error) {
	var sck windows.Handle
	sck = windows.Handle(msck.Socket.Socket())
	server, err := iocp.NewIOCPServer(sck, maxPacketSize, 0, 16384)
	if err != nil {
		return nil, err
	}

	if err := server.PostReceives(1024); err != nil {
		return nil, err
	}

	h := &WinIOHandler{
		server: server,
		dataCh: make(chan []byte, 16384),
		stopCh: make(chan struct{}),
	}
	go h.forwardLoop()
	return h, nil
}

// forwardLoop 持续从 IOCP 队列取出数据，拷贝后归还 Context，再转发到 dataCh。
// 生命周期与 WinIOHandler 相同，在 Stop() 关闭 stopCh 时退出。
func (h *WinIOHandler) forwardLoop() {
	defer close(h.dataCh)
	for {
		select {
		case <-h.stopCh:
			return
		default:
		}

		ctx, ok := h.server.GetDataQueue().TryDequeue()
		if !ok {
			time.Sleep(time.Microsecond)
			continue
		}

		// 拷贝数据后立即归还 Context，使其可接收下一个包
		data := make([]byte, ctx.BytesRecv)
		copy(data, ctx.Data[:ctx.BytesRecv])
		h.server.ReturnContext(ctx)

		select {
		case h.dataCh <- data:
		case <-h.stopCh:
			return
		}
	}
}

func (h *WinIOHandler) Start() {
	h.server.Start()
}

func (h *WinIOHandler) Stop() {
	h.stopOnce.Do(func() {
		h.server.Stop()
		close(h.stopCh)
	})
}

// GetDataQueue 返回持续接收数据的通道，由 forwardLoop 负责写入。
func (h *WinIOHandler) GetDataQueue() chan []byte {
	return h.dataCh
}

// ReturnContext 在 Windows 实现中为空操作：数据已在 forwardLoop 中拷贝并归还 Context。
func (h *WinIOHandler) ReturnContext(ctx interface{}) {}

// TryDequeue 从 dataCh 非阻塞地取出一条数据（[]byte）。
func (h *WinIOHandler) TryDequeue() (interface{}, bool) {
	select {
	case data, ok := <-h.dataCh:
		if !ok {
			return nil, false
		}
		return data, true
	default:
		return nil, false
	}
}
