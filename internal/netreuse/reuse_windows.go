//go:build windows

package netreuse

import (
	"syscall"

	"golang.org/x/sys/windows"
)

const Supported = true

func socketControl(_, _ string, raw syscall.RawConn) error {
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = windows.SetsockoptInt(
			windows.Handle(fd),
			windows.SOL_SOCKET,
			windows.SO_REUSEADDR,
			1,
		)
	}); err != nil {
		return err
	}
	return socketErr
}
