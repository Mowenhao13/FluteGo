//go:build !windows

package pool

import (
	"syscall"
)

func getSocketBufferSize(fd uintptr) (rcvBuf, sndBuf int) {
	rcvBuf, _ = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	sndBuf, _ = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	return
}
