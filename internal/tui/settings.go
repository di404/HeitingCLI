package tui

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/di404/HeitingCLI/internal/audio"
	"github.com/di404/HeitingCLI/internal/config"
)

type settingsState struct {
	cursor  int
	editing bool
	input   textinput.Model
	devices []audio.Device
}

func newSettingsState() settingsState {
	input := textinput.New()
	input.Prompt = ""
	return settingsState{input: input}
}

type devicesMsg []audio.Device

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

	return []settingRow{
		{label: "音频"},
		boolRow("启动时播放", &cfg.AutoPlay, m.saveConfig),
		intRow("音量", &cfg.Volume, 0, 100, volumeStep, applyAudio),
		m.deviceRow(),
		intRow("缓冲秒数", &cfg.CacheSeconds, 1, 30, 1, applyAudio),
		textRow("mpv 路径", &cfg.MPVPath, "mpv", applyAudio),
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
		account,
	}
}

func (m *Model) deviceRow() settingRow {
	devices := m.settings.devices
	value := m.cfg.AudioDevice
	index := -1
	for i, device := range devices {
		if device.Name == value || (value == "" && device.Name == "auto") {
			value, index = device.Description, i
			break
		}
	}
	if value == "" {
		value = "auto"
	}
	return settingRow{
		label: "音频设备",
		value: value,
		adjust: func(delta int) tea.Cmd {
			if len(devices) == 0 {
				return nil
			}
			next := devices[((index+delta)%len(devices)+len(devices))%len(devices)]
			m.cfg.AudioDevice = next.Name
			if next.Name == "auto" {
				m.cfg.AudioDevice = ""
			}
			return m.applyAudio()
		},
	}
}

func (m *Model) openSettings() tea.Cmd {
	m.screen = screenSettings
	m.settings.editing = false
	rows := m.settingRows()
	if !rows[m.settings.cursor].selectable() {
		m.moveSettingsCursor(rows, 1)
	}
	return m.loadDevices()
}

func (m *Model) loadDevices() tea.Cmd {
	path := m.cfg.MPVPath
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		devices, err := audio.ListDevices(ctx, path)
		if err != nil {
			slog.Warn("list audio devices", "err", err)
		}
		return devicesMsg(devices)
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
			return tea.Batch(row.onText(), m.loadDevices())
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
	if msg, ok := msg.(devicesMsg); ok {
		m.settings.devices = msg
		return nil, true
	}
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

// reconnectAccount reopens the danmaku session after the login changes.
func (m *Model) reconnectAccount() tea.Cmd {
	m.user, m.property = nil, nil
	if m.cfg.RoomID == 0 {
		return nil
	}
	return m.openSession(m.cfg.RoomID)
}
