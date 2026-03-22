package io

import (
	"FluteGo/pkg/sock"
)

type IOHandler interface {
	Start()
	Stop()
	GetDataQueue() chan []byte
	ReturnContext(ctx interface{})
	TryDequeue() (interface{}, bool)
}

var ioHandler func(msck *sock.MsSocket, maxPacketSize int) (IOHandler, error)

func NewIOHandler(msck *sock.MsSocket, maxPacketSize int) (IOHandler, error) {
	return ioHandler(msck, maxPacketSize)
}
