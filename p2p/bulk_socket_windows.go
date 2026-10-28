package p2p

import "syscall"

func socketBufferValue(fd uintptr, opt int) (int, error) {
	return syscall.GetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, opt)
}
