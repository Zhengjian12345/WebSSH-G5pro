//go:build !linux
// +build !linux

package pty

func setsane(t Tty) error {
	return nil
}

func setForegroundPgrp(t Pty, pid int) error {
	return nil
}
