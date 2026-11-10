package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/di404/HeitingCLI/internal/bili"
	"github.com/di404/HeitingCLI/internal/config"
)

// hasLogin probes the client's cookies without touching the network: SendDanmu
// rejects a guest before making a request, and the context stops any other.
func hasLogin(c *bili.Client) bool {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return c.SendDanmu(ctx, 1, "x", nil).Error() != "未登录"
}

func hasRow(rows []settingRow, label string) bool {
	for _, row := range rows {
		if row.label == label {
			return true
		}
	}
	return false
}

func toggleAnonymous(t *testing.T, m *Model) {
	t.Helper()
	m.openSettings()
	for i, row := range m.settingRows() {
		if row.label == "匿名模式" {
			m.settings.cursor = i
			m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
			return
		}
	}
	t.Fatal("no anonymous row")
}

func TestAnonymousModeDropsAndRestoresLogin(t *testing.T) {
	m := newTestModel(t)
	const cookies = "SESSDATA=a; bili_jct=b"
	if err := config.SaveCookie(cookies); err != nil {
		t.Fatal(err)
	}
	m.client.SetCookies(cookies)

	toggleAnonymous(t, m)
	if hasLogin(m.client) {
		t.Fatal("login still in use")
	}
	if config.LoadCookie() != cookies {
		t.Fatal("saved login was lost")
	}
	if hasRow(m.settingRows(), "登录") {
		t.Fatal("login offered in anonymous mode")
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Anonymous {
		t.Fatal("anonymous mode was not saved")
	}

	toggleAnonymous(t, m)
	if !hasLogin(m.client) {
		t.Fatal("login not restored")
	}
}

func TestAnonymousModeSkipsLoginPrompt(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Anonymous = true
	m.Init()
	if m.screen == screenLoginPrompt {
		t.Fatal("asked to log in")
	}
}
