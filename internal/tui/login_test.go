package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/di404/HeitingCLI/internal/audio"
	"github.com/di404/HeitingCLI/internal/bili"
	"github.com/di404/HeitingCLI/internal/config"
)

func newTestModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("HEITING_CONFIG_DIR", t.TempDir())
	cfg := config.Default()
	player := audio.NewPlayer(func(context.Context, uint64) (audio.Source, error) {
		return audio.Source{}, audio.ErrOffline
	}, AudioOptions(cfg))
	t.Cleanup(player.Close)
	m := New(cfg, bili.New("", ""), player, 0)
	m.width, m.height = 85, 24
	return m
}

func TestFirstRunAsksToLogin(t *testing.T) {
	m := newTestModel(t)
	m.Init()
	if m.screen != screenLoginPrompt {
		t.Fatalf("screen %d", m.screen)
	}
}

func TestSkippingLoginIsRemembered(t *testing.T) {
	m := newTestModel(t)
	m.Init()
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenRooms {
		t.Fatalf("screen %d", m.screen)
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !saved.LoginPrompted {
		t.Fatal("skip was not saved")
	}
	m.cfg = saved
	m.screen = screenMain
	m.Init()
	if m.screen == screenLoginPrompt {
		t.Fatal("asked again after skipping")
	}
}

func TestSavedLoginSkipsPrompt(t *testing.T) {
	m := newTestModel(t)
	if err := config.SaveCookie("SESSDATA=x"); err != nil {
		t.Fatal(err)
	}
	m.Init()
	if m.screen == screenLoginPrompt {
		t.Fatal("asked to log in with a saved login")
	}
}

func TestCancellingFirstRunLoginReturnsToPrompt(t *testing.T) {
	m := newTestModel(t)
	m.Init()
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenLogin {
		t.Fatalf("screen %d", m.screen)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenLoginPrompt {
		t.Fatalf("screen %d", m.screen)
	}
}

func TestQRAdaptsToWindowHeight(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenLogin
	code := strings.TrimRight(strings.Repeat(strings.Repeat("█", 45)+"\n", 23), "\n")
	m.login.code = &bili.QRCode{Text: code}
	m.login.pngPath = "/tmp/login.png"
	for _, height := range []int{30, 25, 23} {
		m.height = height
		view := m.loginView()
		if !strings.Contains(view, "█") || lipgloss.Height(view) != height {
			t.Fatalf("height %d: code missing or %d rows", height, lipgloss.Height(view))
		}
		if !strings.Contains(view, "esc") {
			t.Fatalf("height %d: hint missing", height)
		}
	}
	m.height = 22
	if view := m.loginView(); strings.Contains(view, "█") || !strings.Contains(view, "login.png") {
		t.Fatalf("expected the image path: %q", view)
	}
}
