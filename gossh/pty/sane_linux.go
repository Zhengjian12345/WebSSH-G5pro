//go:build linux
// +build linux

package pty

import "gossh/sys/unix"

func setsane(t Tty) error {
	termios, err := unix.IoctlGetTermios(int(t.Fd()), unix.TCGETS)
	if err != nil {
		return err
	}

	termios.Iflag |= unix.BRKINT | unix.ICRNL | unix.IXON
	termios.Iflag &^= unix.IGNBRK | unix.IGNCR | unix.INLCR | unix.ISTRIP | unix.IXOFF | unix.PARMRK

	termios.Oflag |= unix.OPOST | unix.ONLCR

	termios.Cflag &^= unix.CSIZE | unix.PARENB
	termios.Cflag |= unix.CREAD | unix.CS8

	termios.Lflag |= unix.ECHO | unix.ECHOCTL | unix.ECHOE | unix.ECHOK | unix.ICANON | unix.IEXTEN | unix.ISIG

	termios.Cc[unix.VINTR] = 3
	termios.Cc[unix.VQUIT] = 28
	termios.Cc[unix.VERASE] = 127
	termios.Cc[unix.VKILL] = 21
	termios.Cc[unix.VEOF] = 4
	termios.Cc[unix.VSTART] = 17
	termios.Cc[unix.VSTOP] = 19
	termios.Cc[unix.VSUSP] = 26
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0

	return unix.IoctlSetTermios(int(t.Fd()), unix.TCSETS, termios)
}

func setForegroundPgrp(t Pty, pid int) error {
	pgid, err := unix.Getpgid(pid)
	if err != nil {
		pgid = pid
	}
	return unix.IoctlSetPointerInt(int(t.Fd()), unix.TIOCSPGRP, pgid)
}
