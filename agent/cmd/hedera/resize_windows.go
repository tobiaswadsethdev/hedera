//go:build windows

package main

import (
	"os"
	"time"

	"golang.org/x/term"
)

// Windows consoles have no SIGWINCH; poll the size instead.
func watchResize(stop <-chan struct{}, fn func()) {
	fd := int(os.Stdout.Fd())
	w, h, _ := term.GetSize(fd)
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if nw, nh, err := term.GetSize(fd); err == nil && (nw != w || nh != h) {
				w, h = nw, nh
				fn()
			}
		}
	}
}
