package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Clicking a tab uses tabRanges, so they must match what renderHeader draws,
// and every tab must stay visible and separated down to narrow terminals.
func TestHeaderTabRangesMatchRender(t *testing.T) {
	for w := 40; w <= 200; w++ {
		m := NewModel(nil, nil)
		m.width, m.height, m.ready = w, 30, true
		line := []rune(ansi.Strip(strings.SplitN(m.renderHeader(), "\n", 2)[0]))
		tb := m.tabBarLayout()
		ranges := m.tabRanges()
		for i, r := range ranges {
			if r[1] > len(line) {
				if w >= 59 {
					t.Fatalf("width %d: tab %d does not fit: %q", w, i+1, string(line))
				}
				continue
			}
			if got := strings.TrimSpace(string(line[r[0]:r[1]])); got != tb.labels[i] {
				t.Fatalf("width %d: tab %d range %v holds %q, want %q (line %q)", w, i+1, r, got, tb.labels[i], string(line))
			}
			if i > 0 && r[0] < len(line) && line[r[0]-1] != ' ' && tb.pad == 0 {
				t.Fatalf("width %d: tabs %d and %d touch: %q", w, i, i+1, string(line))
			}
		}
	}
}
