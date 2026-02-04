//go:build windows

package io

import (
	"FluteGo/pkg/iocp"
	"FluteGo/pkg/sock"

	"golang.org/x/sys/windows"
)

type WinIOHandler struct {
	server *iocp.IOCPServer
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

	return &WinIOHandler{
		server: server,
	}, nil

}

func (h *WinIOHandler) Start() {
	h.server.Start()
}

func (h *WinIOHandler) Stop() {
	h.server.Stop()
}

func (h *WinIOHandler) GetDataQueue() chan []byte {
	// 由于接口返回 chan []byte，但 IOCPServer 返回 MPMCQueueOf[*IOContext]
	// 这里需要创建一个适配器通道
	ch := make(chan []byte, 16384)

	go func() {
		for {
			ctx, ok := h.server.GetDataQueue().TryDequeue()
			if !ok {
				break
			}
			// 提取数据并发送到通道
			data := ctx.Data[:ctx.BytesRecv]
			ch <- data
		}
	}()

	return ch
}

func (h *WinIOHandler) ReturnContext(ctx interface{}) {
	// 类型断言，将 interface{} 转换为 *iocp.IOContext
	if ioCtx, ok := ctx.(*iocp.IOContext); ok {
		h.server.ReturnContext(ioCtx)
	}
}

func (h *WinIOHandler) TryDequeue() (interface{}, bool) {
	ctx, ok := h.server.GetDataQueue().TryDequeue()
	if !ok {
		return nil, false
	}
	return ctx, true
}
