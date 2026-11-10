package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInputMethodIsOnOnlyForDanmuAndWhileUnfocused(t *testing.T) {
	m := newTestModel(t)
	var calls []bool
	m.IMEControl = func(enabled bool) { calls = append(calls, enabled) }
	m.screen = screenMain

	expect := func(step string, want ...bool) {
		t.Helper()
		if len(calls) != len(want) {
			t.Fatalf("%s: calls %v, want %v", step, calls, want)
		}
		for i := range want {
			if calls[i] != want[i] {
				t.Fatalf("%s: calls %v, want %v", step, calls, want)
			}
		}
		calls = nil
	}

	m.Update(tea.WindowSizeMsg{Width: 85, Height: 24})
	expect("shortcut mode", false)

	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	expect("danmu input", true)

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	expect("back to shortcuts", false)

	m.Update(tea.BlurMsg{})
	expect("terminal unfocused", true)

	m.Update(tea.FocusMsg{})
	expect("terminal focused", false)

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	expect("settings keeps it off")
}
