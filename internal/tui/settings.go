package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/di404/HeitingCLI/internal/config"
)

type settingsState struct {
	cursor  int
	editing bool
	input   textinput.Model
}

func newSettingsState() settingsState {
	input := textinput.New()
	input.Prompt = ""
	return settingsState{input: input}
}

// settingRow is one line of the settings screen; a row without any handler is
// a section heading.
type settingRow struct {
	label string
	value string

	adjust func(delta int) tea.Cmd
	toggle bool
	text   *string
	onText func() tea.Cmd
	action func() tea.Cmd
}

func (r settingRow) selectable() bool {
	return r.adjust != nil || r.text != nil || r.action != nil
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

func boolRow(label string, value *bool, apply func() tea.Cmd) settingRow {
	return settingRow{
		label:  label,
		value:  onOff(*value),
		toggle: true,
		adjust: func(int) tea.Cmd {
			*value = !*value
			return apply()
		},
	}
}

func intRow(label string, value *int, low, high, step int, apply func() tea.Cmd) settingRow {
	return settingRow{
		label: label,
		value: strconv.Itoa(*value),
		adjust: func(delta int) tea.Cmd {
			*value = min(max(*value+delta*step, low), high)
			return apply()
		},
	}
}

func textRow(label string, value *string, empty string, apply func() tea.Cmd) settingRow {
	shown := *value
	if shown == "" {
		shown = empty
	}
	return settingRow{label: label, value: shown, text: value, onText: apply}
}

func (m *Model) settingRows() []settingRow {
	cfg := m.cfg
	applyAudio := m.applyAudio
	applyDisplay := func() tea.Cmd {
		m.rerenderDanmu()
		return m.saveConfig()
	}
	applyBuffer := func() tea.Cmd {
		m.trimDanmu()
		m.refreshDanmu()
		return m.saveConfig()
	}
	applyUA := func() tea.Cmd {
		m.client.SetUserAgent(cfg.UserAgent)
		return m.saveConfig()
	}

	account := settingRow{label: "登录", value: "未登录", action: m.startLogin}
	if m.user != nil {
		account = settingRow{label: "退出登录", value: m.user.Name, action: m.logout}
	}

	rows := []settingRow{
		{label: "音频"},
		boolRow("启动时播放", &cfg.AutoPlay, m.saveConfig),
		intRow("音量", &cfg.Volume, 0, 100, volumeStep, applyAudio),
		intRow("缓冲秒数", &cfg.CacheSeconds, 1, 30, 1, applyAudio),
		{label: "弹幕"},
		intRow("缓存条数", &cfg.ChatBuffer, 50, 2000, 50, applyBuffer),
		boolRow("彩色弹幕", &cfg.ColorMode, applyDisplay),
		boolRow("舰长标识", &cfg.ShowShipLevel, applyDisplay),
		boolRow("粉丝牌名称", &cfg.ShowMedalName, applyDisplay),
		boolRow("粉丝牌等级", &cfg.ShowMedalLevel, applyDisplay),
		{label: "界面"},
		boolRow("显示直播标题", &cfg.ShowRoomTitle, m.saveConfig),
		boolRow("显示房间号", &cfg.ShowRoomNumber, m.saveConfig),
		{label: "网络"},
		textRow("User-Agent", &cfg.UserAgent, "默认", applyUA),
		{label: "账号"},
		boolRow("匿名模式", &cfg.Anonymous, m.applyAnonymous),
	}
	if !cfg.Anonymous {
		rows = append(rows, account)
	}
	return rows
}

func (m *Model) openSettings() {
	m.screen = screenSettings
	m.settings.editing = false
	rows := m.settingRows()
	if !rows[m.settings.cursor].selectable() {
		m.moveSettingsCursor(rows, 1)
	}
}

func (m *Model) moveSettingsCursor(rows []settingRow, delta int) {
	for i := m.settings.cursor + delta; i >= 0 && i < len(rows); i += delta {
		if rows[i].selectable() {
			m.settings.cursor = i
			return
		}
	}
}

func (m *Model) handleSettingsKey(msg tea.KeyMsg) tea.Cmd {
	rows := m.settingRows()
	row := rows[m.settings.cursor]
	if m.settings.editing {
		switch msg.String() {
		case "esc":
			m.settings.editing = false
			m.settings.input.Blur()
			return nil
		case "enter":
			*row.text = strings.TrimSpace(m.settings.input.Value())
			m.settings.editing = false
			m.settings.input.Blur()
			return row.onText()
		}
		var cmd tea.Cmd
		m.settings.input, cmd = m.settings.input.Update(msg)
		return cmd
	}

	switch msg.String() {
	case "esc", "q":
		m.screen = screenMain
	case "up", "k":
		m.moveSettingsCursor(rows, -1)
	case "down", "j":
		m.moveSettingsCursor(rows, 1)
	case "left", "h":
		if row.adjust != nil {
			return row.adjust(-1)
		}
	case "right", "l":
		if row.adjust != nil {
			return row.adjust(1)
		}
	case "enter", " ":
		switch {
		case row.toggle:
			return row.adjust(1)
		case row.text != nil:
			m.settings.editing = true
			m.settings.input.SetValue(*row.text)
			m.settings.input.CursorEnd()
			m.settings.input.Width = max(m.width-22, 10)
			return m.settings.input.Focus()
		case row.action != nil:
			return row.action()
		}
	}
	return nil
}

func (m *Model) updateSettings(msg tea.Msg) (tea.Cmd, bool) {
	if m.screen == screenSettings && m.settings.editing {
		var cmd tea.Cmd
		m.settings.input, cmd = m.settings.input.Update(msg)
		return cmd, true
	}
	return nil, false
}

func (m *Model) settingsView() string {
	rows := m.settingRows()
	lines := []string{titleStyle.Render("设置"), ""}
	start, end := scrollWindow(m.settings.cursor, len(rows), m.height-3)
	labelStyle := lipgloss.NewStyle().Width(16)
	for i := start; i < end; i++ {
		row := rows[i]
		if !row.selectable() {
			lines = append(lines, sectionStyle.Render(row.label))
			continue
		}
		cursor, label := "  ", labelStyle.Render(row.label)
		value := row.value
		if i == m.settings.cursor {
			cursor = accentStyle.Render("> ")
			label = selectedStyle.Inherit(labelStyle).Render(row.label)
			if m.settings.editing {
				value = m.settings.input.View()
			} else if row.adjust != nil && !row.toggle {
				value = accentStyle.Render("‹ ") + value + accentStyle.Render(" ›")
			}
		}
		lines = append(lines, truncate(cursor+label+value, m.width))
	}
	hint := "←/→ 调整  enter 切换/编辑  esc 返回"
	if m.settings.editing {
		hint = "enter 保存  esc 取消"
	}
	return fitLines(lines, m.height-1) + "\n" + m.hintView(hint)
}

// logout forgets the account and reconnects as a guest.
func (m *Model) logout() tea.Cmd {
	if err := config.DeleteCookie(); err != nil {
		return m.flash("退出失败: " + err.Error())
	}
	m.client.ClearLogin()
	return m.reconnectAccount()
}

// applyAnonymous swaps the client's identity and reconnects with it. Audio is
// restarted too, because its stream was requested with the previous identity.
func (m *Model) applyAnonymous() tea.Cmd {
	m.client.ClearLogin()
	if !m.cfg.Anonymous {
		m.client.SetCookies(config.LoadCookie())
	}
	if m.listening && m.cfg.RoomID != 0 {
		m.player.Play(m.cfg.RoomID)
	}
	return tea.Batch(m.saveConfig(), m.reconnectAccount())
}

// reconnectAccount reopens the danmaku session after the login changes.
func (m *Model) reconnectAccount() tea.Cmd {
	m.user, m.property = nil, nil
	if m.cfg.RoomID == 0 {
		return nil
	}
	return m.openSession(m.cfg.RoomID)
}
