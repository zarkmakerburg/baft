package main

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Terminal capability detection for the interactive menu (HQ A3). Direct
// commands (baft status, baft doctor --json, ...) never use any of this: they
// print no splash and no escape sequence. Only the menu does, and only on a
// real terminal.

// Color modes.
const (
	colorFull  = "FULL_COLOR" // 24-bit
	color256   = "256_COLOR"
	colorMono  = "MONOCHROME" // a terminal, but no color escapes (NO_COLOR, or no color support)
	colorPlain = "PLAIN"      // not a terminal, or TERM=dumb: no graphics at all
)

// Header layouts by width.
const (
	layoutWide    = "WIDE"    // >= 100 columns
	layoutNormal  = "NORMAL"  // 70-99
	layoutCompact = "COMPACT" // < 70
)

type termCaps struct {
	TTY     bool
	Color   string
	Unicode bool
	Width   int
	Layout  string
}

// detectTerm decides what the terminal can show. It is pure: the environment
// and the facts about the terminal are passed in.
func detectTerm(getenv func(string) string, isTTY bool, width int) termCaps {
	c := termCaps{TTY: isTTY, Width: width}
	if c.Width <= 0 {
		if n, err := strconv.Atoi(getenv("COLUMNS")); err == nil && n > 0 {
			c.Width = n
		} else {
			c.Width = 80
		}
	}
	switch {
	case c.Width >= 100:
		c.Layout = layoutWide
	case c.Width >= 70:
		c.Layout = layoutNormal
	default:
		c.Layout = layoutCompact
	}
	term := getenv("TERM")
	switch {
	case !isTTY || term == "dumb":
		c.Color = colorPlain
	case getenv("NO_COLOR") != "":
		c.Color = colorMono
	case strings.EqualFold(getenv("COLORTERM"), "truecolor") || strings.EqualFold(getenv("COLORTERM"), "24bit"):
		c.Color = colorFull
	case strings.Contains(term, "256color"):
		c.Color = color256
	default:
		c.Color = colorMono
	}
	loc := strings.ToLower(getenv("LC_ALL") + " " + getenv("LC_CTYPE") + " " + getenv("LANG"))
	c.Unicode = c.Color != colorPlain && (strings.Contains(loc, "utf-8") || strings.Contains(loc, "utf8"))
	return c
}

// style turns semantic roles into escape sequences for the detected mode.
// With no color every method returns its text untouched.
type style struct{ caps termCaps }

type rgb struct{ r, g, b int }

// The BAFT palette (HQ): primary, highlight and deep gold, with neutrals and
// status colors. The brand itself stays gold; only status uses other hues.
var (
	goldPrimary   = rgb{0xD4, 0xAF, 0x37}
	goldHighlight = rgb{0xFF, 0xD7, 0x6A}
	goldDeep      = rgb{0x9B, 0x6A, 0x12}
	neutralLight  = rgb{0xE6, 0xE6, 0xE6}
	neutralGray   = rgb{0x8C, 0x8C, 0x8C}
	neutralDark   = rgb{0x44, 0x44, 0x44}
	statusGreen   = rgb{0x50, 0xC8, 0x78}
	statusRed     = rgb{0xE6, 0x50, 0x50}
)

// nearest xterm-256 code for each palette color.
var code256 = map[rgb]int{
	goldPrimary: 178, goldHighlight: 221, goldDeep: 136,
	neutralLight: 254, neutralGray: 245, neutralDark: 238,
	statusGreen: 78, statusRed: 203,
}

func (s style) paint(c rgb, bold bool, text string) string {
	if text == "" {
		return text
	}
	var seq string
	switch s.caps.Color {
	case colorFull:
		seq = "38;2;" + strconv.Itoa(c.r) + ";" + strconv.Itoa(c.g) + ";" + strconv.Itoa(c.b)
	case color256:
		seq = "38;5;" + strconv.Itoa(code256[c])
	default:
		return text
	}
	if bold {
		seq = "1;" + seq
	}
	return "\x1b[" + seq + "m" + text + "\x1b[0m"
}

func (s style) gold(t string) string      { return s.paint(goldPrimary, false, t) }
func (s style) goldBold(t string) string  { return s.paint(goldPrimary, true, t) }
func (s style) highlight(t string) string { return s.paint(goldHighlight, true, t) }
func (s style) deep(t string) string      { return s.paint(goldDeep, false, t) }
func (s style) light(t string) string     { return s.paint(neutralLight, false, t) }
func (s style) dim(t string) string       { return s.paint(neutralGray, false, t) }

// status colors a health word. The word itself is always shown, so color is
// never the only indicator.
func (s style) status(word string) string {
	switch word {
	case "HEALTHY", "UP", "ACTIVE", "MANAGED":
		return s.paint(statusGreen, true, word)
	case "DEGRADED", "RECOVERING", "WARNING":
		return s.paint(goldPrimary, true, word)
	case "DOWN", "FAILED", "FAILING":
		return s.paint(statusRed, true, word)
	}
	return s.paint(neutralGray, true, word)
}

// clean makes a string that came from outside (hostname, config, unit text)
// safe to print: control characters, including ESC, never reach the terminal.
func clean(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
