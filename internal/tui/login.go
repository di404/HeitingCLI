package tui

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/di404/HeitingCLI/internal/bili"
	"github.com/di404/HeitingCLI/internal/config"
)

const loginPollInterval = 2 * time.Second

type loginState struct {
	generation int
	code       *bili.QRCode
	status     bili.QRStatus
	err        string
	pngPath    string
}

type (
	qrReadyMsg struct {
		generation int
		code       *bili.QRCode
		err        error
	}
	loginTickMsg struct{ generation int }
	loginPollMsg struct {
		generation int
		status     bili.QRStatus
		cookies    string
		err        error
	}
)

func (m *Model) startLogin() tea.Cmd {
	m.screen = screenLogin
	return m.requestQR()
}

func (m *Model) requestQR() tea.Cmd {
	dir, err := config.CacheDir()
	if err != nil {
		m.login.err = err.Error()
		return nil
	}
	m.login.generation++
	m.login.code, m.login.err = nil, ""
	m.login.status = bili.QRNotScanned
	m.login.pngPath = filepath.Join(dir, "login.png")

	generation, png, inverse, client := m.login.generation, m.login.pngPath, !m.darkTheme, m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		code, err := client.GenerateQR(ctx, inverse, png)
		return qrReadyMsg{generation: generation, code: code, err: err}
	}
}

func loginTick(generation int) tea.Cmd {
	return tea.Tick(loginPollInterval, func(time.Time) tea.Msg {
		return loginTickMsg{generation: generation}
	})
}

func (m *Model) pollLogin() tea.Cmd {
	generation, key, client := m.login.generation, m.login.code.Key, m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		status, cookies, err := client.PollQR(ctx, key)
		return loginPollMsg{generation: generation, status: status, cookies: cookies, err: err}
	}
}

func (m *Model) handleLoginKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		m.login.generation++ // Discards anything still in flight.
		m.screen = screenSettings
	case "r":
		return m.requestQR()
	}
	return nil
}

func (m *Model) updateLogin(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case qrReadyMsg:
		if msg.generation != m.login.generation {
			return nil, true
		}
		if msg.err != nil {
			m.login.err = msg.err.Error()
			return nil, true
		}
		m.login.code = msg.code
		return loginTick(msg.generation), true
	case loginTickMsg:
		if msg.generation != m.login.generation || m.login.code == nil {
			return nil, true
		}
		return m.pollLogin(), true
	case loginPollMsg:
		if msg.generation != m.login.generation {
			return nil, true
		}
		if msg.err != nil {
			m.login.err = msg.err.Error()
			return loginTick(msg.generation), true
		}
		m.login.err = ""
		m.login.status = msg.status
		switch msg.status {
		case bili.QRExpired:
			return m.requestQR(), true
		case bili.QRConfirmed:
			return m.finishLogin(msg.cookies), true
		}
		return loginTick(msg.generation), true
	}
	return nil, false
}

func (m *Model) finishLogin(cookies string) tea.Cmd {
	m.login.generation++
	m.login.code = nil
	m.client.SetCookies(cookies)
	m.screen = screenSettings
	var flash tea.Cmd
	if err := config.SaveCookie(cookies); err != nil {
		flash = m.flash("保存登录状态失败: " + err.Error())
	}
	return tea.Batch(flash, m.reconnectAccount())
}

func (m *Model) loginView() string {
	var body string
	switch {
	case m.login.err != "":
		body = redStyle.Render(m.login.err)
	case m.login.code == nil:
		body = dimStyle.Render("获取二维码中")
	default:
		tip := "使用哔哩哔哩 App 扫码"
		if m.login.status == bili.QRScanned {
			tip = "在手机上确认登录"
		}
		parts := []string{tip, strings.TrimRight(m.login.code.Text, "\n")}
		if lipgloss.Height(m.login.code.Text)+2 > m.height {
			parts = []string{tip, m.login.pngPath}
		}
		body = lipgloss.JoinVertical(lipgloss.Center, parts...)
	}
	return lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center, body) +
		"\n" + m.hintView("r 刷新  esc 返回")
}
