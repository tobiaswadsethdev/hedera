//go:build !windows

package main

func detachConsole() {}

func enableVTOutput(uintptr) func() { return func() {} }
