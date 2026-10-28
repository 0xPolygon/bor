//go:build !windows

package p2p

import "syscall"

func socketBufferValue(fd uintptr, opt int) (int, error) {
	return syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, opt)
}
