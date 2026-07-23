//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package netreuse

import "syscall"

const Supported = false

func socketControl(_, _ string, _ syscall.RawConn) error {
	return nil
}
