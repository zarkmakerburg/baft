package main

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// The BAFT terminal mark (HQ A3): an ANSI/Unicode/ASCII interpretation of the
// official logo, not a pixel copy: a golden braided B, black technical cable in
// its bowls, vertical golden network lines and golden nodes. Gold on the
// terminal's own background. No image, no font, no network, no dependency.
//
// Cell classes in the rasters:
//
//	R rope (gold, woven)   K cable (dark)   N node (bright gold)   L network line (deep gold)
var markWide = []string{
	"L  N                    ",
	"L   RRRRRRRRRRRRRR      ",
	"L N RRKKKKKKKKKKKRRR    ",
	"L   RRKKKKKKKKKKKKKRR   ",
	"LN  RRKKKKKKKKKKKKRRR   ",
	"L   RRKKKKKKKKKKRRR     ",
	"L   RRRRRRRRRRRRR       ",
	"L N RRKKKKKKKKKKKRRR    ",
	"L   RRKKKKKKKKKKKKKRRR  ",
	"LN  RRKKKKKKKKKKKKKRRR  ",
	"L   RRKKKKKKKKKKKKRRR   ",
	"L N RRRRRRRRRRRRRRR     ",
}

var markSmall = []string{
	"L N RRRRRRR  ",
	"L   RRKKKRR  ",
	"LN  RRRRRR   ",
	"L   RRKKKKRR ",
	"L N RRRRRRR  ",
}

// glyph returns the character and the class for a raster cell.
func glyph(caps termCaps, class byte, x, y int) (string, byte) {
	switch class {
	case 'R':
		if caps.Unicode {
			// A two-tone weave: the pattern shifts with the row, like a twist.
			if (x+y)%2 == 0 {
				return "▓", 'R'
			}
			return "▒", 'r'
		}
		if (x+y)%2 == 0 {
			return "#", 'R'
		}
		return "=", 'r'
	case 'K':
		if caps.Unicode {
			return "░", 'K'
		}
		return ".", 'K'
	case 'N':
		if caps.Unicode {
			return "●", 'N'
		}
		return "o", 'N'
	case 'L':
		if caps.Unicode {
			return "│", 'L'
		}
		return "|", 'L'
	}
	return " ", ' '
}

func (s style) cell(class byte, g string) string {
	switch class {
	case 'R':
		return s.gold(g)
	case 'r':
		return s.deep(g)
	case 'K':
		return s.paint(neutralDark, false, g)
	case 'N':
		return s.highlight(g)
	case 'L':
		return s.deep(g)
	}
	return g
}

// renderMark returns the mark as lines of fixed display width.
func renderMark(caps termCaps, raster []string) []string {
	st := style{caps}
	out := make([]string, len(raster))
	for y, row := range raster {
		var b strings.Builder
		for x := 0; x < len(row); x++ {
			g, c := glyph(caps, row[x], x, y)
			b.WriteString(st.cell(c, g))
		}
		out[y] = b.String()
	}
	return out
}

// headerInfo is the context shown under the mark. Every field is already
// cleaned of control characters.
type headerInfo struct {
	Version, Node, Role, Health, Release string
}

// displayWidth counts printed columns of a string with no escape sequences.
func displayWidth(s string) int { return utf8.RuneCountInString(s) }

// stripANSI removes color sequences, to measure a styled line.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func pad(styled string, width int) string {
	if n := width - displayWidth(stripANSI(styled)); n > 0 {
		return styled + strings.Repeat(" ", n)
	}
	return styled
}

// renderHeader prints the BAFT header for the menu. Layouts:
//
//	WIDE    (>=100 columns): the large mark beside the title and the context
//	NORMAL  (70-99):         a small mark above the title and the context, in a box
//	COMPACT (<70):           two lines, for a phone over SSH
//
// PLAIN (TERM=dumb) prints the two compact lines without any glyphs.
func renderHeader(w io.Writer, caps termCaps, h headerInfo) {
	st := style{caps}
	title := "B  A  F  T"
	tagline := "Resilient Network Fabric"
	rows := [][2]string{{"Version", h.Version}, {"Node", h.Node}, {"Role", h.Role}, {"Health", ""}, {"Release", h.Release}}
	infoLine := func(k, v string) string {
		if k == "Health" {
			return pad(st.dim(k), 9) + st.status(h.Health)
		}
		return pad(st.dim(k), 9) + st.light(v)
	}

	if caps.Color == colorPlain || caps.Layout == layoutCompact {
		sep, dot := " | ", " | "
		if caps.Unicode {
			sep, dot = " ◈ ", " · "
		}
		if caps.Color == colorPlain {
			fmt.Fprintf(w, "BAFT %s%s%s\n%s%s%s%s%s\n", h.Version, " ", "", h.Node, dot, h.Role, dot, h.Health)
			return
		}
		fmt.Fprintf(w, "%s%s%s\n%s%s%s%s%s\n", st.goldBold("BAFT"), sep, st.light(h.Version), st.light(h.Node), dot, st.light(h.Role), dot, st.status(h.Health))
		return
	}

	h1, v, tl, tr, bl, br := "─", "│", "╭", "╮", "╰", "╯"
	if !caps.Unicode {
		h1, v, tl, tr, bl, br = "-", "|", "+", "+", "+", "+"
	}
	var inner []string
	if caps.Layout == layoutWide {
		mark := renderMark(caps, markWide)
		text := []string{"", "", st.goldBold(title), st.light(tagline), "", ""}
		for _, r := range rows {
			text = append(text, infoLine(r[0], r[1]))
		}
		for i := range mark {
			t := ""
			if i < len(text) {
				t = text[i]
			}
			inner = append(inner, " "+pad(mark[i], 26)+" "+t)
		}
	} else {
		for _, m := range renderMark(caps, markSmall) {
			inner = append(inner, " "+m)
		}
		inner = append(inner, "", " "+st.goldBold(title), " "+st.light(tagline), "")
		for _, r := range rows {
			inner = append(inner, " "+infoLine(r[0], r[1]))
		}
	}
	width := 0
	for _, l := range inner {
		if n := displayWidth(stripANSI(l)); n > width {
			width = n
		}
	}
	width += 2
	if max := caps.Width - 2; width > max && max > 20 {
		width = max
	}
	fmt.Fprintln(w, st.deep(tl+strings.Repeat(h1, width)+tr))
	for _, l := range inner {
		fmt.Fprintln(w, st.deep(v)+truncateStyled(pad(l, width), width)+st.deep(v))
	}
	fmt.Fprintln(w, st.deep(bl+strings.Repeat(h1, width)+br))
}

// truncateStyled cuts a possibly styled line to width printed columns.
func truncateStyled(s string, width int) string {
	if displayWidth(stripANSI(s)) <= width {
		return s
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(s) && n < width; {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			b.WriteString(s[i : j+1])
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += size
		n++
	}
	b.WriteString("\x1b[0m")
	return b.String()
}
