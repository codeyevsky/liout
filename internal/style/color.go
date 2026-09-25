// Package style holds the 256-colour SGR codes and helpers the TUI uses.
// Palette echoes githubFlex (violet family) but tuned for liout (blue accent).
package style

import (
	"os"

	"golang.org/x/term"
)

// On reports whether stdout is an interactive terminal (colours + animation).
var On = term.IsTerminal(int(os.Stdout.Fd()))

const (
	Lilac  = "38;5;111" // headings
	Blue   = "38;5;39"  // primary accent
	Deep   = "38;5;27"  // rules / frames
	Sky    = "38;5;117"
	Green  = "38;5;114"
	Red    = "38;5;203"
	Yellow = "38;5;221"
	Cyan   = "38;5;80"
	Gray   = "38;5;245"
	Dim    = "2"
	Bold   = "1"
)

// Tint wraps s in an SGR code, or returns it plain when colour is off.
func Tint(code, s string) string {
	if !On {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Hl renders a selected item as a solid blue bar with white bold text.
func Hl(s string) string {
	if !On {
		return "\x1b[7m " + s + " \x1b[0m"
	}
	return "\x1b[48;5;27;38;5;231;1m " + s + " \x1b[0m"
}
