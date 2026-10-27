package main

import (
	"strings"

	"rsc.io/qr"
)

// renderQR draws text as a QR code using half-block characters, two modules
// per character cell. Colours are explicit, so it scans on dark and light
// terminal themes alike.
func renderQR(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 2
	black := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	const (
		fgDark, fgLight = "\x1b[30m", "\x1b[97m"
		bgDark, bgLight = "\x1b[40m", "\x1b[107m"
		reset           = "\x1b[0m"
	)
	var b strings.Builder
	for y := -quiet; y < code.Size+quiet; y += 2 {
		for x := -quiet; x < code.Size+quiet; x++ {
			top, bottom := black(x, y), black(x, y+1)
			if top {
				b.WriteString(fgDark)
			} else {
				b.WriteString(fgLight)
			}
			if bottom {
				b.WriteString(bgDark)
			} else {
				b.WriteString(bgLight)
			}
			b.WriteString("▀")
		}
		b.WriteString(reset + "\n")
	}
	return b.String(), nil
}
