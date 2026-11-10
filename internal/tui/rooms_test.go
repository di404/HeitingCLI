package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/di404/HeitingCLI/internal/config"
)

func runes(text string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)}
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func newSavedRoomModel(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	m.cfg.LoginPrompted = true
	m.cfg.RoomID = 2
	m.cfg.Rooms = []config.Room{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	return m
}

func TestStartupShowsRoomListWithoutJoining(t *testing.T) {
	m := newSavedRoomModel(t)
	m.Init()
	if m.screen != screenRooms {
		t.Fatalf("screen %d", m.screen)
	}
	if m.session != nil {
		t.Fatal("joined a room without being asked")
	}
	if m.rooms.adding {
		t.Fatal("opened the add prompt with rooms saved")
	}
	if m.sortedRooms()[m.rooms.cursor].ID != 2 {
		t.Fatal("last room not preselected")
	}
}

func TestStartupWithRoomArgumentJoinsIt(t *testing.T) {
	m := newSavedRoomModel(t)
	m.initialRoom = 7
	m.Init()
	defer m.Close()
	if m.screen != screenMain || m.session == nil || m.cfg.RoomID != 7 {
		t.Fatalf("screen %d, session %v, room %d", m.screen, m.session, m.cfg.RoomID)
	}
}

func TestStartupWithNoRoomsAsksForOne(t *testing.T) {
	m := newTestModel(t)
	m.cfg.LoginPrompted = true
	m.Init()
	if m.screen != screenRooms || !m.rooms.adding {
		t.Fatalf("screen %d, adding %v", m.screen, m.rooms.adding)
	}
}

func TestAnonymousToggleInRoomListDoesNotJoin(t *testing.T) {
	m := newSavedRoomModel(t)
	const cookies = "SESSDATA=a; bili_jct=b"
	if err := config.SaveCookie(cookies); err != nil {
		t.Fatal(err)
	}
	m.client.SetCookies(cookies)
	m.Init()

	m.handleKey(runes("n"))
	if m.session != nil {
		t.Fatal("toggling anonymous joined a room")
	}
	if !m.cfg.Anonymous || hasLogin(m.client) {
		t.Fatal("anonymous mode not applied")
	}
	if !strings.Contains(m.roomsView(), "匿名 开") {
		t.Fatal("anonymous state not shown")
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Anonymous {
		t.Fatal("anonymous mode was not saved")
	}

	m.handleKey(runes("n"))
	if m.cfg.Anonymous || !hasLogin(m.client) {
		t.Fatal("login not restored")
	}
}

func TestEnterJoinsSelectedRoom(t *testing.T) {
	m := newSavedRoomModel(t)
	m.Init()
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	defer m.Close()
	if m.screen != screenMain || m.session == nil {
		t.Fatalf("screen %d, session %v", m.screen, m.session)
	}
}

func TestLeavingRoomListBeforeJoining(t *testing.T) {
	m := newSavedRoomModel(t)
	m.Init()
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenRooms {
		t.Fatal("esc left the list with no room to return to")
	}
	if !isQuit(m.handleKey(runes("q"))) {
		t.Fatal("q did not quit")
	}
}

func TestLeavingRoomListWhileInRoomReturnsToIt(t *testing.T) {
	m := newSavedRoomModel(t)
	m.Init()
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	defer m.Close()
	m.handleKey(runes("r"))
	if isQuit(m.handleKey(runes("q"))) || m.screen != screenMain {
		t.Fatalf("screen %d", m.screen)
	}
}

func TestAddingRoomSelectsItWithoutJoining(t *testing.T) {
	m := newTestModel(t)
	m.cfg.LoginPrompted = true
	m.Init()
	m.updateRooms(roomAddedMsg{room: config.Room{ID: 9, Name: "c"}})
	if m.screen != screenRooms || m.session != nil {
		t.Fatalf("screen %d, session %v", m.screen, m.session)
	}
	if m.sortedRooms()[m.rooms.cursor].ID != 9 {
		t.Fatal("new room not selected")
	}
}
