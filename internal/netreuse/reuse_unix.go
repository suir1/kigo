//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package netreuse

import (
	"syscall"

	"golang.org/x/sys/unix"
)

const Supported = true

func socketControl(_, _ string, raw syscall.RawConn) error {
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
			socketErr = err
			return
		}
		if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
			socketErr = err
		}
	}); err != nil {
		return err
	}
	return socketErr
}
