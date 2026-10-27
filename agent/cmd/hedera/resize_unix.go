//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func watchResize(stop <-chan struct{}, fn func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)
	for {
		select {
		case <-stop:
			return
		case <-ch:
			fn()
		}
	}
}
