package bili

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
)

const (
	verPlain     uint16 = 0
	verHeartbeat uint16 = 1
	verZlib      uint16 = 2
	verBrotli    uint16 = 3

	opHeartbeat uint32 = 2
	opMessage   uint32 = 5
	opAuth      uint32 = 7

	headerSize   = 16
	maxFrameSize = 16 << 20

	heartbeatInterval = 30 * time.Second
	readTimeout       = 70 * time.Second
	writeTimeout      = 10 * time.Second
)

// Events emitted by a Session.
type (
	// EventRoom carries the room and account details after each (re)connect.
	EventRoom struct {
		Info     RoomInfo
		Anchor   string
		User     *User
		Property *DanmuProperty
	}
	EventConnected    struct{}
	EventDisconnected struct{ Err error }
	EventDanmu        struct{ Danmu *Danmu }
	EventLive         struct{}
	EventPreparing    struct{}
	EventTitle        struct{ Title string }
	// EventOnline carries the number of viewers currently in the room.
	EventOnline struct{ Count uint64 }
)

// Session keeps a danmaku connection open for one room, reconnecting until
// closed.
type Session struct {
	client *Client
	roomID uint64
	events chan any
	cancel context.CancelFunc
}

// OpenSession starts connecting to a room in the background.
func (c *Client) OpenSession(roomID uint64) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{client: c, roomID: roomID, events: make(chan any, 64), cancel: cancel}
	go s.run(ctx)
	return s
}

// Events is closed once the session has stopped.
func (s *Session) Events() <-chan any { return s.events }

// Close stops the session.
func (s *Session) Close() { s.cancel() }

func (s *Session) emit(ctx context.Context, event any) {
	select {
	case s.events <- event:
	case <-ctx.Done():
	}
}

func (s *Session) run(ctx context.Context) {
	defer close(s.events)
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := s.connect(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("danmaku connection lost", "room", s.roomID, "err", err)
		s.emit(ctx, EventDisconnected{Err: err})
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

type danmuServer struct {
	Token    string `json:"token"`
	HostList []struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"host_list"`
}

func (c *Client) getDanmuServer(ctx context.Context, roomID uint64) (*danmuServer, error) {
	rawURL, err := c.signedURL(ctx, "https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo",
		url.Values{"id": {strconv.FormatUint(roomID, 10)}, "type": {"0"}, "web_location": {"444.8"}})
	if err != nil {
		return nil, err
	}
	info := new(danmuServer)
	if err := c.getJSON(ctx, rawURL, info); err != nil {
		return nil, err
	}
	return info, nil
}

func (s *Session) connect(ctx context.Context) error {
	c := s.client
	if err := c.ensureBuvid(ctx); err != nil {
		return err
	}
	user, err := c.CurrentUser(ctx)
	if err != nil {
		return err
	}
	info, err := c.GetRoomInfo(ctx, s.roomID)
	if err != nil {
		return err
	}
	anchor, err := c.GetAnchorName(ctx, info.RoomID)
	if err != nil {
		slog.Warn("get anchor name", "err", err)
	}
	var property *DanmuProperty
	var uid uint64
	if user != nil {
		uid = user.UID
		if property, err = c.getDanmuProperty(ctx, info.RoomID); err != nil {
			slog.Warn("get danmu property", "err", err)
		}
	}
	server, err := c.getDanmuServer(ctx, info.RoomID)
	if err != nil {
		return err
	}
	s.emit(ctx, EventRoom{Info: *info, Anchor: anchor, User: user, Property: property})

	conn, err := dialServer(ctx, server)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	reader := bufio.NewReaderSize(conn, 64*1024)
	if err := s.authenticate(conn, reader, uid, info.RoomID, server.Token); err != nil {
		return err
	}
	s.emit(ctx, EventConnected{})

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go heartbeatLoop(connCtx, conn)
	if user != nil {
		go s.roomHeartbeatLoop(connCtx, info.RoomID)
	}

	for {
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return err
		}
		frame, err := readFrame(reader)
		if err != nil {
			return err
		}
		s.handleFrame(ctx, frame)
	}
}

func dialServer(ctx context.Context, server *danmuServer) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	var lastErr error = errors.New("没有可用的弹幕服务器")
	for _, host := range server.HostList {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host.Host, strconv.Itoa(host.Port)))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (s *Session) authenticate(conn net.Conn, reader io.Reader, uid, roomID uint64, token string) error {
	body, err := json.Marshal(map[string]any{
		"uid":      uid,
		"roomid":   roomID,
		"protover": 3,
		"buvid":    s.client.cookie("buvid3"),
		"platform": "web",
		"type":     2,
		"key":      token,
	})
	if err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	if _, err := conn.Write(packFrame(verHeartbeat, opAuth, 1, body)); err != nil {
		return err
	}
	frame, err := readFrame(reader)
	if err != nil {
		return err
	}
	var reply struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(frame.body, &reply); err != nil {
		return fmt.Errorf("弹幕认证响应解析失败: %w", err)
	}
	if reply.Code != 0 {
		return fmt.Errorf("弹幕认证失败 (%d)", reply.Code)
	}
	return conn.SetDeadline(time.Time{})
}

func heartbeatLoop(ctx context.Context, conn net.Conn) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for seq := uint32(2); ; seq++ {
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := conn.Write(packFrame(verHeartbeat, opHeartbeat, seq, []byte("[object Object]"))); err != nil {
			conn.Close()
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// roomHeartbeatLoop reports the logged-in viewer as watching the room.
func (s *Session) roomHeartbeatLoop(ctx context.Context, roomID uint64) {
	interval := 20
	for {
		select {
		case <-time.After(time.Duration(interval) * time.Second):
		case <-ctx.Done():
			return
		}
		next, err := s.client.sendRoomHeartbeat(ctx, roomID, interval)
		if err != nil && ctx.Err() == nil {
			slog.Warn("room heartbeat", "err", err)
		}
		interval = next
	}
}

func encodeHeartbeat(interval int, roomID uint64) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%d|1|0", interval, roomID)))
}

type frame struct {
	version uint16
	op      uint32
	body    []byte
}

func packFrame(version uint16, op, seq uint32, body []byte) []byte {
	out := make([]byte, headerSize+len(body))
	binary.BigEndian.PutUint32(out[0:], uint32(len(out)))
	binary.BigEndian.PutUint16(out[4:], headerSize)
	binary.BigEndian.PutUint16(out[6:], version)
	binary.BigEndian.PutUint32(out[8:], op)
	binary.BigEndian.PutUint32(out[12:], seq)
	copy(out[headerSize:], body)
	return out
}

func readFrame(r io.Reader) (frame, error) {
	var header [headerSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return frame{}, err
	}
	size := binary.BigEndian.Uint32(header[0:])
	hdr := uint32(binary.BigEndian.Uint16(header[4:]))
	if hdr < headerSize || size < hdr || size > maxFrameSize {
		return frame{}, fmt.Errorf("invalid frame: size=%d header=%d", size, hdr)
	}
	if _, err := io.CopyN(io.Discard, r, int64(hdr-headerSize)); err != nil {
		return frame{}, err
	}
	body := make([]byte, size-hdr)
	if _, err := io.ReadFull(r, body); err != nil {
		return frame{}, err
	}
	return frame{
		version: binary.BigEndian.Uint16(header[6:]),
		op:      binary.BigEndian.Uint32(header[8:]),
		body:    body,
	}, nil
}

func (s *Session) handleFrame(ctx context.Context, f frame) {
	for _, body := range decodeMessages(f) {
		if event := parseCommand(body); event != nil {
			s.emit(ctx, event)
		}
	}
}

// decodeMessages returns the JSON bodies inside a frame, unpacking compressed
// frames, which contain further frames.
func decodeMessages(f frame) [][]byte {
	if f.op != opMessage {
		return nil
	}
	var reader io.Reader
	switch f.version {
	case verZlib:
		zr, err := zlib.NewReader(bytes.NewReader(f.body))
		if err != nil {
			slog.Warn("zlib frame", "err", err)
			return nil
		}
		defer zr.Close()
		reader = zr
	case verBrotli:
		reader = brotli.NewReader(bytes.NewReader(f.body))
	default:
		return [][]byte{f.body}
	}
	buffered := bufio.NewReader(reader)
	var messages [][]byte
	for {
		inner, err := readFrame(buffered)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Warn("nested frame", "err", err)
			}
			return messages
		}
		messages = append(messages, decodeMessages(inner)...)
	}
}

func parseCommand(body []byte) any {
	var msg struct {
		Cmd  string          `json:"cmd"`
		Info []any           `json:"info"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		slog.Warn("message json", "err", err)
		return nil
	}
	cmd, _, _ := strings.Cut(msg.Cmd, ":")
	switch cmd {
	case "DANMU_MSG":
		if danmu := parseDanmu(msg.Info); danmu != nil {
			return EventDanmu{Danmu: danmu}
		}
	case "LIVE":
		return EventLive{}
	case "PREPARING":
		return EventPreparing{}
	case "ONLINE_RANK_COUNT":
		var data struct {
			OnlineCount uint64 `json:"online_count"`
		}
		if json.Unmarshal(msg.Data, &data) == nil && data.OnlineCount > 0 {
			return EventOnline{Count: data.OnlineCount}
		}
	case "ROOM_CHANGE":
		var data struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(msg.Data, &data) == nil && data.Title != "" {
			return EventTitle{Title: data.Title}
		}
	}
	return nil
}
