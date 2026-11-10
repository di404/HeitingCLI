// Package tui is the terminal interface.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/di404/HeitingCLI/internal/audio"
	"github.com/di404/HeitingCLI/internal/bili"
	"github.com/di404/HeitingCLI/internal/config"
)

type screen int

const (
	screenMain screen = iota
	screenRooms
	screenSettings
	screenLogin
	screenLoginPrompt
)

const (
	defaultDanmuLength = 20
	statusDuration     = 4 * time.Second
	volumeStep         = 5
)

type danmuLine struct {
	danmu    *bili.Danmu
	rendered string
}

// Model is the root of the interface.
type Model struct {
	cfg         *config.Config
	client      *bili.Client
	player      *audio.Player
	initialRoom uint64
	darkTheme   bool

	screen        screen
	width, height int

	session      *bili.Session
	room         *bili.RoomInfo
	online       uint64
	user         *bili.User
	property     *bili.DanmuProperty
	connected    bool
	newRoom      bool
	sessionError string

	audioStatus audio.Status
	listening   bool

	danmu      []*danmuLine
	viewport   viewport.Model
	input      textinput.Model
	inputFocus bool
	follow     bool

	status   string
	statusID int

	rooms    roomsState
	settings settingsState
	login    loginState

	// IMEControl, if set, turns the system input method on or off.
	IMEControl  func(enabled bool)
	asciiForced bool
	termBlurred bool
}

// New builds the interface. initialRoom, if non-zero, overrides the saved room.
func New(cfg *config.Config, client *bili.Client, player *audio.Player, initialRoom uint64) *Model {
	input := textinput.New()
	input.Prompt = "> "
	input.CharLimit = defaultDanmuLength

	view := viewport.New(0, 0)
	view.KeyMap = viewport.KeyMap{
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		Up:       key.NewBinding(key.WithKeys("up", "k")),
		Down:     key.NewBinding(key.WithKeys("down", "j")),
	}

	return &Model{
		cfg:         cfg,
		client:      client,
		player:      player,
		initialRoom: initialRoom,
		darkTheme:   lipgloss.HasDarkBackground(),
		listening:   cfg.AutoPlay,
		viewport:    view,
		input:       input,
		follow:      true,
		rooms:       newRoomsState(),
		settings:    newSettingsState(),
	}
}

// Close releases the danmaku connection.
func (m *Model) Close() {
	if m.session != nil {
		m.session.Close()
	}
}

// AudioOptions extracts the player settings from the config.
func AudioOptions(cfg *config.Config) audio.Options {
	return audio.Options{
		CacheSeconds: cfg.CacheSeconds,
		Volume:       cfg.Volume,
		Muted:        cfg.Muted,
	}
}

type (
	sessionMsg struct {
		session *bili.Session
		event   any
	}
	playerMsg    audio.Status
	statusClear  struct{ id int }
	danmuSentMsg struct{ err error }
)

func waitSession(s *bili.Session) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-s.Events()
		if !ok {
			return nil
		}
		return sessionMsg{session: s, event: event}
	}
}

func waitPlayer(p *audio.Player) tea.Cmd {
	return func() tea.Msg { return playerMsg(<-p.Updates()) }
}

// Init asks a first-time user to log in, or moves on to choosing a room.
func (m *Model) Init() tea.Cmd {
	background := tea.Batch(waitPlayer(m.player), textinput.Blink)
	if !m.cfg.Anonymous && config.LoadCookie() == "" && !m.cfg.LoginPrompted {
		m.screen = screenLoginPrompt
		return background
	}
	return tea.Batch(background, m.enterApp())
}

// enterApp opens the room named on the command line. Otherwise it shows the room
// list: joining a room is visible to others, so it needs an explicit choice.
func (m *Model) enterApp() tea.Cmd {
	if m.initialRoom != 0 {
		m.screen = screenMain
		return m.switchRoom(m.initialRoom)
	}
	cmd := m.openRooms()
	if len(m.cfg.Rooms) == 0 {
		return tea.Batch(cmd, m.startRoomAdd())
	}
	return cmd
}

// switchRoom connects to a room and starts its audio if the user is listening.
func (m *Model) switchRoom(roomID uint64) tea.Cmd {
	m.room, m.user, m.property = nil, nil, nil
	m.online = 0
	m.connected, m.sessionError = false, ""
	m.newRoom = true
	m.danmu = nil
	m.follow = true
	m.refreshDanmu()

	cmd := m.openSession(roomID)
	if m.listening {
		m.player.Play(roomID)
	} else {
		m.player.Stop()
	}
	if m.cfg.RoomID != roomID {
		m.cfg.RoomID = roomID
		return tea.Batch(cmd, m.saveConfig())
	}
	return cmd
}

func (m *Model) openSession(roomID uint64) tea.Cmd {
	if m.session != nil {
		m.session.Close()
	}
	m.session = m.client.OpenSession(roomID)
	return waitSession(m.session)
}

func (m *Model) saveConfig() tea.Cmd {
	if err := m.cfg.Save(); err != nil {
		slog.Error("save config", "err", err)
		return m.flash("保存配置失败: " + err.Error())
	}
	return nil
}

func (m *Model) flash(text string) tea.Cmd {
	m.statusID++
	m.status = text
	id := m.statusID
	return tea.Tick(statusDuration, func(time.Time) tea.Msg { return statusClear{id: id} })
}

// Update routes messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.syncIME()
	return m, cmd
}

// syncIME leaves the input method on only where Chinese can be typed. Everywhere
// else a key is a shortcut, or part of a room number or User-Agent. It also goes
// back on when the terminal loses focus, because the input source is system-wide.
func (m *Model) syncIME() {
	if m.IMEControl == nil {
		return
	}
	forceASCII := !m.termBlurred && !(m.screen == screenMain && m.inputFocus)
	if forceASCII != m.asciiForced {
		m.asciiForced = forceASCII
		m.IMEControl(!forceASCII)
	}
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.FocusMsg:
		m.termBlurred = false
		return nil
	case tea.BlurMsg:
		m.termBlurred = true
		return nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return tea.Quit
		}
		return m.handleKey(msg)
	case tea.MouseMsg:
		if m.screen == screenMain {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			m.follow = m.viewport.AtBottom()
			return cmd
		}
		return nil
	case statusClear:
		if msg.id == m.statusID {
			m.status = ""
		}
		return nil
	case playerMsg:
		m.audioStatus = audio.Status(msg)
		return waitPlayer(m.player)
	case sessionMsg:
		if msg.session != m.session {
			return nil
		}
		return tea.Batch(waitSession(msg.session), m.handleSessionEvent(msg.event))
	case danmuSentMsg:
		if msg.err != nil {
			return m.flash("发送失败: " + msg.err.Error())
		}
		return nil
	}

	if cmd, handled := m.updateRooms(msg); handled {
		return cmd
	}
	if cmd, handled := m.updateSettings(msg); handled {
		return cmd
	}
	if cmd, handled := m.updateLogin(msg); handled {
		return cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

func (m *Model) handleSessionEvent(event any) tea.Cmd {
	switch event := event.(type) {
	case bili.EventRoom:
		info := event.Info
		m.room, m.user, m.property = &info, event.User, event.Property
		m.sessionError = ""
		m.input.CharLimit = defaultDanmuLength
		if m.property != nil && m.property.Danmu.Length > 0 {
			m.input.CharLimit = m.property.Danmu.Length
		}
		// Only the first connect saves the room, so a reconnect cannot bring
		// back a room the user just deleted from the list.
		if m.newRoom {
			m.newRoom = false
			return m.rememberRoom(info.RoomID, event.Anchor)
		}
	case bili.EventConnected:
		m.connected, m.sessionError = true, ""
	case bili.EventDisconnected:
		m.connected = false
		if event.Err != nil {
			m.sessionError = event.Err.Error()
		}
	case bili.EventDanmu:
		m.addDanmu(event.Danmu)
	case bili.EventLive:
		if m.room != nil {
			m.room.LiveStatus = 1
		}
		m.player.Wake()
	case bili.EventPreparing:
		if m.room != nil {
			m.room.LiveStatus = 0
		}
	case bili.EventOnline:
		m.online = event.Count
	case bili.EventTitle:
		if m.room != nil {
			m.room.Title = event.Title
		}
	}
	return nil
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch m.screen {
	case screenRooms:
		return m.handleRoomsKey(msg)
	case screenSettings:
		return m.handleSettingsKey(msg)
	case screenLogin:
		return m.handleLoginKey(msg)
	case screenLoginPrompt:
		return m.handleLoginPromptKey(msg)
	}
	if m.inputFocus {
		return m.handleInputKey(msg)
	}

	switch msg.String() {
	case "q":
		return tea.Quit
	case "tab", "i", "enter":
		m.inputFocus = true
		return m.input.Focus()
	case "m":
		m.cfg.Muted = !m.cfg.Muted
		return m.applyAudio()
	case "+", "=":
		return m.changeVolume(volumeStep)
	case "-", "_":
		return m.changeVolume(-volumeStep)
	case "p":
		m.listening = !m.listening
		if m.listening {
			m.player.Play(m.cfg.RoomID)
		} else {
			m.player.Stop()
		}
		return nil
	case "r":
		return m.openRooms()
	case "s":
		m.openSettings()
		return nil
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	m.follow = m.viewport.AtBottom()
	return cmd
}

func (m *Model) handleInputKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "tab", "esc":
		m.inputFocus = false
		m.input.Blur()
		return nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return nil
		}
		if m.cfg.Anonymous {
			return m.flash("匿名模式下不能发送弹幕")
		}
		if m.user == nil || m.room == nil {
			return m.flash("登录后才能发送弹幕")
		}
		m.input.Reset()
		roomID, property := m.room.RoomID, m.property
		client := m.client
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return danmuSentMsg{err: client.SendDanmu(ctx, roomID, text, property)}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

func (m *Model) changeVolume(delta int) tea.Cmd {
	m.cfg.Volume = min(max(m.cfg.Volume+delta, 0), config.MaxVolume)
	return m.applyAudio()
}

// applyAudio pushes audio settings to the player and persists them.
func (m *Model) applyAudio() tea.Cmd {
	m.player.Configure(AudioOptions(m.cfg))
	return m.saveConfig()
}

func (m *Model) layout() {
	m.viewport.Width = max(m.width, 0)
	m.viewport.Height = max(m.height-5, 1)
	m.input.Width = max(m.width-lipgloss.Width(m.input.Prompt)-1, 1)
	for _, line := range m.danmu {
		line.rendered = ""
	}
	m.refreshDanmu()
}

func (m *Model) addDanmu(d *bili.Danmu) {
	m.danmu = append(m.danmu, &danmuLine{danmu: d})
	m.trimDanmu()
	m.refreshDanmu()
}

func (m *Model) trimDanmu() {
	if excess := len(m.danmu) - m.cfg.ChatBuffer; excess > 0 {
		m.danmu = append([]*danmuLine(nil), m.danmu[excess:]...)
	}
}

// rerenderDanmu redraws every message after a display setting changes.
func (m *Model) rerenderDanmu() {
	for _, line := range m.danmu {
		line.rendered = ""
	}
	m.refreshDanmu()
}

func (m *Model) refreshDanmu() {
	var content strings.Builder
	rows := 0
	for _, line := range m.danmu {
		if line.rendered == "" {
			line.rendered = renderDanmu(line.danmu, m.cfg, m.viewport.Width)
		}
		rows += strings.Count(line.rendered, "\n") + 1
	}
	// Keep messages anchored to the bottom while the view is not yet full.
	if padding := m.viewport.Height - rows; padding > 0 {
		content.WriteString(strings.Repeat("\n", padding))
	}
	for i, line := range m.danmu {
		if i > 0 {
			content.WriteByte('\n')
		}
		content.WriteString(line.rendered)
	}
	m.viewport.SetContent(content.String())
	if m.follow {
		m.viewport.GotoBottom()
	}
}

// View draws the current screen.
func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	switch m.screen {
	case screenRooms:
		return m.roomsView()
	case screenSettings:
		return m.settingsView()
	case screenLogin:
		return m.loginView()
	case screenLoginPrompt:
		return m.loginPromptView()
	}
	return m.mainView()
}

func (m *Model) mainView() string {
	rule := dimStyle.Render(strings.Repeat("─", max(m.width, 0)))
	return strings.Join([]string{
		m.headerView(),
		rule,
		m.viewport.View(),
		rule,
		m.input.View(),
		m.hintView("tab 输入  m 静音  +/- 音量  p 播放/停止  r 房间  s 设置  q 退出"),
	}, "\n")
}

func (m *Model) hintView(hint string) string {
	if m.status != "" {
		return truncate(accentStyle.Render(m.status), m.width)
	}
	return truncate(dimStyle.Render(hint), m.width)
}

func (m *Model) headerView() string {
	right := m.audioView()
	if m.online > 0 {
		right = dimStyle.Render("在线 "+formatCount(m.online)) + "  " + right
	}
	if m.cfg.Anonymous {
		right = accentStyle.Render("匿名") + "  " + right
	}
	if !m.connected && m.sessionError != "" {
		right = redStyle.Render("弹幕重连中") + "  " + right
	}
	var left string
	if m.room != nil {
		var parts []string
		if m.cfg.ShowRoomTitle {
			parts = append(parts, titleStyle.Render(m.room.Title))
		}
		if m.cfg.ShowRoomNumber {
			parts = append(parts, dimStyle.Render(fmt.Sprintf("%d", m.displayRoomID())))
		}
		left = strings.Join(parts, " ")
	} else if m.sessionError != "" {
		left = redStyle.Render(m.sessionError)
	} else {
		left = dimStyle.Render("连接中")
	}
	rightWidth := lipgloss.Width(right)
	left = truncate(left, m.width-rightWidth-1)
	gap := max(m.width-lipgloss.Width(left)-rightWidth, 0)
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) displayRoomID() uint64 {
	if m.room.ShortID != 0 {
		return m.room.ShortID
	}
	return m.room.RoomID
}

func (m *Model) audioView() string {
	var state string
	switch m.audioStatus.State {
	case audio.Stopped:
		state = dimStyle.Render("已停止")
	case audio.Resolving, audio.Buffering:
		state = dimStyle.Render("缓冲中")
	case audio.Playing:
		state = greenStyle.Render("播放中")
	case audio.Offline:
		state = dimStyle.Render("未开播")
	case audio.Retrying:
		state = redStyle.Render("音频重连中")
	case audio.Failed:
		state = redStyle.Render(m.audioStatus.Err)
	}
	if m.cfg.Muted {
		return state + "  " + accentStyle.Render("静音")
	}
	return state + "  " + fmt.Sprintf("音量 %d", m.cfg.Volume)
}

func formatCount(n uint64) string {
	if n < 10000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1f万", float64(n)/10000)
}
