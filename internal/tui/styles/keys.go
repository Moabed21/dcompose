package styles

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines the global keybindings. View-specific keys are handled by
// each view and documented in the help overlay.
type KeyMap struct {
	Quit    key.Binding
	Help    key.Binding
	Refresh key.Binding
	Tasks   key.Binding
	NextTab key.Binding
	PrevTab key.Binding
	Tabs    []key.Binding
}

// DefaultKeyMap returns the default global keybindings.
func DefaultKeyMap() KeyMap {
	tabs := make([]key.Binding, 8)
	for i := range tabs {
		k := string(rune('1' + i))
		tabs[i] = key.NewBinding(key.WithKeys(k), key.WithHelp(k, "tab"))
	}
	return KeyMap{
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Refresh: key.NewBinding(key.WithKeys("R", "ctrl+r"), key.WithHelp("R", "refresh")),
		Tasks:   key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "task output")),
		NextTab: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
		PrevTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev tab")),
		Tabs:    tabs,
	}
}
