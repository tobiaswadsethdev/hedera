package main

import "errors"

func reexec(string) error {
	return errors.New("restarting in place is not supported on Windows")
}
