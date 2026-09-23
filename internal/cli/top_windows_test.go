//go:build windows

package cli

import (
	"fmt"
	"strings"
	"testing"
)

// rowsOf counts the console rows text takes at a width, wrapping as the
// console does.
func rowsOf(text string, cols int) int {
	n := 0
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		w := len([]rune(line))
		if w <= cols {
			n++
			continue
		}
		n += (w + cols - 1) / cols
	}
	return n
}

// A frame taller than the window would scroll its own header away on every
// redraw, which is what --watch with a wslc session did in a 30-row console.
func TestFitFrameKeepsTheHeaderOnScreen(t *testing.T) {
	var lines []string
	for i := 0; i < 34; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	got := fitFrame(strings.Join(lines, "\n")+"\n", 120, 30, "footer")
	// One row to spare, so the last newline does not scroll the window.
	if n := rowsOf(got, 120); n > 29 {
		t.Errorf("%d rows for a 30-row window:\n%s", n, got)
	}
	if !strings.HasPrefix(got, "line 0\n") {
		t.Errorf("the header is gone:\n%s", got)
	}
	if !strings.Contains(got, "more line(s)") || !strings.HasSuffix(got, "footer\n") {
		t.Errorf("the cut is not said, or the footer is missing:\n%s", got)
	}
}

// Measured in a 120x30 console: 26 lines fit on paper, but the long notes
// wrap, and the frame scrolled its header off anyway.
func TestFitFrameCountsWrappedLines(t *testing.T) {
	var lines []string
	for i := 0; i < 26; i++ {
		line := fmt.Sprintf("line %d", i)
		if i%5 == 4 {
			line += " " + strings.Repeat("x", 200)
		}
		lines = append(lines, line)
	}
	got := fitFrame(strings.Join(lines, "\n")+"\n", 120, 30, "footer")
	if n := rowsOf(got, 120); n > 29 {
		t.Errorf("%d rows for a 30-row window:\n%s", n, got)
	}
	if !strings.HasPrefix(got, "line 0\n") || !strings.Contains(got, "more line(s)") {
		t.Errorf("got:\n%s", got)
	}
}

func TestFitFrameLeavesAFrameThatFits(t *testing.T) {
	got := fitFrame("a\nb\n", 120, 30, "footer")
	if got != "a\nb\n\nfooter\n" {
		t.Errorf("got %q", got)
	}
	// An unknown height cuts nothing.
	if strings.Contains(fitFrame(strings.Repeat("x\n", 100), 0, 0, "f"), "more line") {
		t.Error("cut without knowing the height")
	}
}
