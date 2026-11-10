package tui

import (
	"context"
	"path/filepath"
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
	firstRun   bool
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

	generation, png, dark, client := m.login.generation, m.login.pngPath, m.darkTheme, m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		code, err := client.GenerateQR(ctx, dark, png)
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
		if m.login.firstRun {
			m.login.firstRun = false
			m.screen = screenLoginPrompt
		}
	case "r":
		return m.requestQR()
	}
	return nil
}

func (m *Model) handleLoginPromptKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		m.login.firstRun = true
		return m.startLogin()
	case "esc":
		return m.finishLoginPrompt()
	}
	return nil
}

func (m *Model) finishLoginPrompt() tea.Cmd {
	m.cfg.LoginPrompted = true
	return tea.Batch(m.saveConfig(), m.enterApp())
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
	var flash tea.Cmd
	if err := config.SaveCookie(cookies); err != nil {
		flash = m.flash("保存登录状态失败: " + err.Error())
	}
	if m.login.firstRun {
		m.login.firstRun = false
		return tea.Batch(flash, m.finishLoginPrompt())
	}
	m.screen = screenSettings
	return tea.Batch(flash, m.reconnectAccount())
}

func (m *Model) loginView() string {
	const hint = "r 刷新  esc 返回"
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
		code := m.login.code.Text
		rows, columns := lipgloss.Height(code), lipgloss.Width(code)
		switch {
		case rows+2 <= m.height:
			body = lipgloss.JoinVertical(lipgloss.Center, tip, code)
		case rows <= m.height && columns+2+lipgloss.Width(hint) <= m.width:
			// No row to spare: the code takes the full height.
			side := lipgloss.JoinVertical(lipgloss.Left, tip, "", m.hintView(hint))
			return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
				lipgloss.JoinHorizontal(lipgloss.Center, code, "  ", side))
		default:
			body = lipgloss.JoinVertical(lipgloss.Center, tip, "窗口太小，放大窗口显示二维码，或打开图片：", m.login.pngPath)
		}
	}
	return lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center, body) +
		"\n" + m.hintView(hint)
}

func (m *Model) loginPromptView() string {
	body := lipgloss.JoinVertical(lipgloss.Center, titleStyle.Render("登录哔哩哔哩"), "登录后可以发送弹幕、查看完整用户名")
	return lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center, body) +
		"\n" + m.hintView("enter 扫码登录  esc 稍后再登录")
}
