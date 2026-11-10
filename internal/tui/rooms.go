package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/di404/HeitingCLI/internal/bili"
	"github.com/di404/HeitingCLI/internal/config"
)

const roomInfoConcurrency = 4

type roomStatus struct {
	live  bool
	title string
}

type roomsState struct {
	cursor   int
	statuses map[uint64]roomStatus
	adding   bool
	busy     bool
	input    textinput.Model
}

func newRoomsState() roomsState {
	input := textinput.New()
	input.Prompt = "房间号或链接: "
	return roomsState{statuses: map[uint64]roomStatus{}, input: input}
}

type (
	roomStatusesMsg map[uint64]roomStatus
	roomAddedMsg    struct {
		room config.Room
		err  error
	}
)

func (m *Model) openRooms() tea.Cmd {
	m.screen = screenRooms
	m.rooms.cursor = 0
	if index := slices.IndexFunc(m.sortedRooms(), func(r config.Room) bool { return r.ID == m.cfg.RoomID }); index >= 0 {
		m.rooms.cursor = index
	}
	return m.fetchRoomStatuses()
}

// sortedRooms lists rooms that are live first, otherwise in saved order.
func (m *Model) sortedRooms() []config.Room {
	rooms := slices.Clone(m.cfg.Rooms)
	slices.SortStableFunc(rooms, func(a, b config.Room) int {
		liveA, liveB := m.rooms.statuses[a.ID].live, m.rooms.statuses[b.ID].live
		switch {
		case liveA && !liveB:
			return -1
		case liveB && !liveA:
			return 1
		}
		return 0
	})
	return rooms
}

func (m *Model) fetchRoomStatuses() tea.Cmd {
	rooms := slices.Clone(m.cfg.Rooms)
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var (
			mu       sync.Mutex
			wg       sync.WaitGroup
			limiter  = make(chan struct{}, roomInfoConcurrency)
			statuses = make(roomStatusesMsg, len(rooms))
		)
		for _, room := range rooms {
			wg.Add(1)
			go func() {
				defer wg.Done()
				limiter <- struct{}{}
				defer func() { <-limiter }()
				info, err := client.GetRoomInfo(ctx, room.ID)
				if err != nil {
					return
				}
				mu.Lock()
				statuses[room.ID] = roomStatus{live: info.IsLive(), title: info.Title}
				mu.Unlock()
			}()
		}
		wg.Wait()
		return statuses
	}
}

func (m *Model) startRoomAdd() tea.Cmd {
	m.rooms.adding = true
	m.rooms.input.Reset()
	return m.rooms.input.Focus()
}

func (m *Model) stopRoomAdd() {
	m.rooms.adding = false
	m.rooms.input.Blur()
}

func (m *Model) handleRoomsKey(msg tea.KeyMsg) tea.Cmd {
	if m.rooms.adding {
		return m.handleRoomAddKey(msg)
	}
	rooms := m.sortedRooms()
	switch msg.String() {
	case "esc", "q":
		switch {
		case m.session != nil:
			m.screen = screenMain
		case msg.String() == "q":
			return tea.Quit
		}
	case "n":
		m.cfg.Anonymous = !m.cfg.Anonymous
		return m.applyAnonymous()
	case "up", "k":
		m.rooms.cursor = max(m.rooms.cursor-1, 0)
	case "down", "j":
		m.rooms.cursor = min(m.rooms.cursor+1, max(len(rooms)-1, 0))
	case "a":
		return m.startRoomAdd()
	case "r":
		return m.fetchRoomStatuses()
	case "d", "x":
		if m.rooms.cursor < len(rooms) {
			target := rooms[m.rooms.cursor].ID
			m.cfg.Rooms = slices.DeleteFunc(m.cfg.Rooms, func(r config.Room) bool { return r.ID == target })
			m.rooms.cursor = min(m.rooms.cursor, max(len(m.cfg.Rooms)-1, 0))
			return m.saveConfig()
		}
	case "enter":
		if m.rooms.cursor < len(rooms) {
			target := rooms[m.rooms.cursor].ID
			m.screen = screenMain
			if target != m.cfg.RoomID || m.session == nil {
				return m.switchRoom(target)
			}
		}
	}
	return nil
}

func (m *Model) handleRoomAddKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.stopRoomAdd()
		return nil
	case "enter":
		id, ok := bili.ParseRoomID(strings.TrimSpace(m.rooms.input.Value()))
		if !ok {
			return m.flash("无效的房间号或链接")
		}
		m.stopRoomAdd()
		m.rooms.busy = true
		client := m.client
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			info, err := client.GetRoomInfo(ctx, id)
			if err != nil {
				return roomAddedMsg{err: err}
			}
			name, err := client.GetAnchorName(ctx, info.RoomID)
			if err != nil || name == "" {
				name = fmt.Sprint(info.RoomID)
			}
			return roomAddedMsg{room: config.Room{ID: info.RoomID, Name: name}}
		}
	}
	var cmd tea.Cmd
	m.rooms.input, cmd = m.rooms.input.Update(msg)
	return cmd
}

// rememberRoom adds a room to the saved list, or fills in its real name.
func (m *Model) rememberRoom(id uint64, name string) tea.Cmd {
	if name == "" {
		name = fmt.Sprint(id)
	}
	changed := false
	if index := slices.IndexFunc(m.cfg.Rooms, func(r config.Room) bool { return r.ID == id }); index >= 0 {
		if m.cfg.Rooms[index].Name != name {
			m.cfg.Rooms[index].Name = name
			changed = true
		}
	} else {
		m.cfg.Rooms = append(m.cfg.Rooms, config.Room{ID: id, Name: name})
		changed = true
	}
	if m.cfg.RoomID != id {
		// A short number was given; store the real one.
		m.cfg.RoomID = id
		changed = true
	}
	if changed {
		return m.saveConfig()
	}
	return nil
}

func (m *Model) updateRooms(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case roomStatusesMsg:
		rooms := m.sortedRooms()
		var selected uint64
		if m.rooms.cursor < len(rooms) {
			selected = rooms[m.rooms.cursor].ID
		}
		m.rooms.statuses = msg
		if index := slices.IndexFunc(m.sortedRooms(), func(r config.Room) bool { return r.ID == selected }); index >= 0 {
			m.rooms.cursor = index
		}
		return nil, true
	case roomAddedMsg:
		m.rooms.busy = false
		if msg.err != nil {
			return m.flash("添加失败: " + msg.err.Error()), true
		}
		if !slices.ContainsFunc(m.cfg.Rooms, func(r config.Room) bool { return r.ID == msg.room.ID }) {
			m.cfg.Rooms = append(m.cfg.Rooms, msg.room)
		}
		if index := slices.IndexFunc(m.sortedRooms(), func(r config.Room) bool { return r.ID == msg.room.ID }); index >= 0 {
			m.rooms.cursor = index
		}
		return tea.Batch(m.saveConfig(), m.fetchRoomStatuses()), true
	}
	if m.screen == screenRooms && m.rooms.adding {
		var cmd tea.Cmd
		m.rooms.input, cmd = m.rooms.input.Update(msg)
		return cmd, true
	}
	return nil, false
}

func (m *Model) roomsView() string {
	anonymous := dimStyle.Render("匿名 " + onOff(m.cfg.Anonymous))
	if m.cfg.Anonymous {
		anonymous = accentStyle.Render("匿名 " + onOff(m.cfg.Anonymous))
	}
	lines := []string{titleStyle.Render("房间") + "  " + anonymous, ""}
	rooms := m.sortedRooms()
	listHeight := m.height - 5
	start, end := scrollWindow(m.rooms.cursor, len(rooms), listHeight)
	for i := start; i < end; i++ {
		lines = append(lines, truncate(m.roomRow(rooms[i], i == m.rooms.cursor), m.width))
	}
	body := fitLines(lines, m.height-2)

	last := ""
	switch {
	case m.rooms.adding:
		last = m.rooms.input.View()
	case m.rooms.busy:
		last = dimStyle.Render("添加中")
	}
	hint := "enter 进入  a 添加  d 删除  r 刷新  n 匿名  esc 返回"
	if m.session == nil {
		hint = "enter 进入  a 添加  d 删除  r 刷新  n 匿名  q 退出"
	}
	if m.rooms.adding {
		hint = "enter 确认  esc 取消"
	}
	return body + "\n" + truncate(last, m.width) + "\n" + m.hintView(hint)
}

func (m *Model) roomRow(room config.Room, selected bool) string {
	status := m.rooms.statuses[room.ID]
	dot := dimStyle.Render("○")
	if status.live {
		dot = greenStyle.Render("●")
	}
	cursor, nameStyle := "  ", lipgloss.NewStyle()
	if selected {
		cursor, nameStyle = accentStyle.Render("> "), selectedStyle
	}
	name := nameStyle.Width(18).MaxWidth(18).Render(truncate(room.Name, 17))
	id := dimStyle.Width(12).Render(fmt.Sprint(room.ID))
	return cursor + dot + " " + name + id + dimStyle.Render(status.title)
}
