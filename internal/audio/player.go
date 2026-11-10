// Package audio plays a live room's sound through a background mpv process.
//
// mpv keeps the terminal untouched (--no-terminal), discards the video track,
// and is controlled over its JSON IPC protocol.
package audio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrOffline is returned by a Resolver when the room is not broadcasting.
var ErrOffline = errors.New("直播未开始")

// Source is a stream mpv can open.
type Source struct {
	URL       string
	UserAgent string
	Referer   string
	// UseYTDL lets mpv resolve URL through yt-dlp.
	UseYTDL bool
}

// Resolver finds the current stream for a room.
type Resolver func(ctx context.Context, roomID uint64) (Source, error)

// State is what the player is doing.
type State int

const (
	Stopped State = iota
	Resolving
	Buffering
	Playing
	Offline
	Retrying
	Failed
)

// Status is published whenever the player changes.
type Status struct {
	State State
	Err   string
}

// Options are the user-configurable playback settings.
type Options struct {
	MPVPath      string
	Device       string
	CacheSeconds int
	Volume       int
	Muted        bool
}

// Player supervises one mpv process at a time.
type Player struct {
	resolve Resolver
	updates chan Status
	wake    chan struct{}

	mu     sync.Mutex
	opts   Options
	status Status
	room   uint64
	cancel context.CancelFunc
	done   chan struct{}
	conn   net.Conn
}

// NewPlayer creates a stopped player.
func NewPlayer(resolve Resolver, opts Options) *Player {
	return &Player{
		resolve: resolve,
		opts:    opts,
		updates: make(chan Status, 1),
		wake:    make(chan struct{}, 1),
	}
}

// Updates delivers the latest status; intermediate ones may be skipped.
func (p *Player) Updates() <-chan Status { return p.updates }

// Status returns the current state.
func (p *Player) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// Play starts playing a room, replacing whatever was playing.
func (p *Player) Play(roomID uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.startLocked(roomID)
}

func (p *Player) startLocked(roomID uint64) {
	if p.cancel != nil {
		p.cancel()
	}
	previous := p.done
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.room, p.cancel, p.done = roomID, cancel, done
	p.setStatusLocked(Status{State: Resolving})
	go func() {
		defer close(done)
		if previous != nil {
			<-previous
		}
		p.run(ctx, roomID)
	}()
}

// Stop ends playback.
func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	p.room, p.cancel = 0, nil
	p.setStatusLocked(Status{State: Stopped})
}

// Close stops playback and waits for mpv to exit.
func (p *Player) Close() {
	p.Stop()
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Wake retries immediately if the player is waiting for the stream to start.
func (p *Player) Wake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Configure applies new options, restarting mpv only if it has to.
func (p *Player) Configure(opts Options) {
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.opts
	p.opts = opts
	if p.room != 0 && (old.MPVPath != opts.MPVPath || old.CacheSeconds != opts.CacheSeconds) {
		p.startLocked(p.room)
		return
	}
	if old.Volume != opts.Volume {
		p.sendLocked("set_property", "volume", opts.Volume)
	}
	if old.Muted != opts.Muted {
		p.sendLocked("set_property", "mute", opts.Muted)
	}
	if old.Device != opts.Device {
		p.sendLocked("set_property", "audio-device", deviceName(opts.Device))
	}
}

func deviceName(device string) string {
	if device == "" {
		return "auto"
	}
	return device
}

func (p *Player) setStatusLocked(status Status) {
	p.status = status
	select {
	case <-p.updates:
	default:
	}
	p.updates <- status
}

// setStatus ignores updates from a run that has been cancelled, so a replaced
// run cannot overwrite the state of its successor.
func (p *Player) setStatus(ctx context.Context, status Status) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() == nil {
		p.setStatusLocked(status)
	}
}

func (p *Player) sendLocked(args ...any) {
	if p.conn == nil {
		return
	}
	data, err := json.Marshal(map[string]any{"command": args})
	if err != nil {
		return
	}
	_ = p.conn.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := p.conn.Write(append(data, '\n')); err != nil {
		slog.Warn("mpv ipc write", "err", err)
	}
}

func (p *Player) wait(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-p.wake:
	case <-ctx.Done():
	}
}

const (
	offlinePoll = 30 * time.Second
	minBackoff  = 2 * time.Second
	maxBackoff  = 30 * time.Second
	stableAfter = 30 * time.Second
)

func (p *Player) run(ctx context.Context, roomID uint64) {
	backoff := minBackoff
	for ctx.Err() == nil {
		p.setStatus(ctx, Status{State: Resolving})
		src, err := p.resolve(ctx, roomID)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, ErrOffline):
			p.setStatus(ctx, Status{State: Offline})
			p.wait(ctx, offlinePoll)
			continue
		case err != nil:
			slog.Warn("resolve stream", "room", roomID, "err", err)
			p.setStatus(ctx, Status{State: Retrying, Err: err.Error()})
			p.wait(ctx, backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		started := time.Now()
		err = p.playOnce(ctx, src)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errNoMPV) {
			p.setStatus(ctx, Status{State: Failed, Err: err.Error()})
			return
		}
		slog.Warn("mpv exited", "room", roomID, "err", err)
		if time.Since(started) > stableAfter {
			backoff = minBackoff
		}
		p.setStatus(ctx, Status{State: Retrying})
		p.wait(ctx, backoff)
		backoff = min(backoff*2, maxBackoff)
	}
}

var errNoMPV = errors.New("未找到 mpv")

func binaryName(path string) string {
	if path == "" {
		return "mpv"
	}
	return path
}

func buildArgs(src Source, opts Options) []string {
	args := []string{
		"--no-config",
		"--no-terminal",
		"--vid=no",
		"--audio-display=no",
		"--force-window=no",
		"--idle=no",
		"--network-timeout=15",
		"--cache=yes",
		"--cache-secs=" + strconv.Itoa(opts.CacheSeconds),
		"--volume=" + strconv.Itoa(opts.Volume),
		"--mute=" + yesNo(opts.Muted),
		"--audio-device=" + deviceName(opts.Device),
	}
	if src.UseYTDL {
		// mpv resolves the room page through yt-dlp; the video track is
		// discarded, so the smallest format wastes the least bandwidth.
		args = append(args, "--ytdl=yes", "--ytdl-format=worst")
	} else {
		args = append(args, "--ytdl=no", "--user-agent="+src.UserAgent, "--referrer="+src.Referer)
	}
	return append(args, src.URL)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (p *Player) playOnce(ctx context.Context, src Source) error {
	p.mu.Lock()
	opts := p.opts
	p.mu.Unlock()

	endpoint, err := newIPCEndpoint()
	if err != nil {
		return err
	}
	defer endpoint.close()

	args := append(endpoint.args(), buildArgs(src, opts)...)
	cmd := exec.CommandContext(ctx, binaryName(opts.MPVPath), args...)
	endpoint.prepare(cmd)
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return errNoMPV
		}
		return fmt.Errorf("启动 mpv 失败: %w", err)
	}
	defer func() { _ = cmd.Wait() }()
	endpoint.started()

	conn, err := endpoint.connect(ctx)
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	defer conn.Close()

	p.mu.Lock()
	if ctx.Err() != nil {
		p.mu.Unlock()
		_ = cmd.Process.Kill()
		return ctx.Err()
	}
	p.conn = conn
	p.setStatusLocked(Status{State: Buffering})
	p.sendLocked("observe_property", 1, "core-idle")
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.conn == conn {
			p.conn = nil
		}
		p.mu.Unlock()
	}()

	return p.readEvents(ctx, conn)
}

type mpvEvent struct {
	Event     string          `json:"event"`
	Name      string          `json:"name"`
	Data      json.RawMessage `json:"data"`
	Reason    string          `json:"reason"`
	FileError string          `json:"file_error"`
}

// readEvents returns when mpv closes the connection, i.e. when it exits.
func (p *Player) readEvents(ctx context.Context, conn net.Conn) error {
	var endErr error
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var event mpvEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch event.Event {
		case "property-change":
			if event.Name == "core-idle" {
				var idle bool
				if json.Unmarshal(event.Data, &idle) == nil {
					state := Buffering
					if !idle {
						state = Playing
					}
					p.setStatus(ctx, Status{State: state})
				}
			}
		case "end-file":
			if event.Reason == "error" {
				endErr = fmt.Errorf("mpv: %s", event.FileError)
			}
		}
	}
	if endErr != nil {
		return endErr
	}
	return errors.New("音频流已结束")
}

// Device is an output listed by mpv.
type Device struct {
	Name        string
	Description string
}

var devicePattern = regexp.MustCompile(`^\s*'(.+)' \((.*)\)\s*$`)

// ListDevices asks mpv for the available audio outputs.
func ListDevices(ctx context.Context, mpvPath string) ([]Device, error) {
	output, err := exec.CommandContext(ctx, binaryName(mpvPath), "--no-config", "--audio-device=help").Output()
	if err != nil {
		return nil, err
	}
	return parseDevices(string(output)), nil
}

// parseDevices keeps one entry per physical device, since mpv lists each once
// per audio backend. On macOS mpv lists coreaudio first but auto-selects
// avfoundation, and its coreaudio output can fail or hang on open, so the
// avfoundation entry wins.
func parseDevices(output string) []Device {
	var devices []Device
	index := map[string]int{}
	for _, line := range strings.Split(output, "\n") {
		match := devicePattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		device := Device{Name: match[1], Description: match[2]}
		if i, seen := index[device.Description]; seen {
			if strings.HasPrefix(device.Name, "avfoundation/") {
				devices[i] = device
			}
			continue
		}
		index[device.Description] = len(devices)
		devices = append(devices, device)
	}
	return devices
}
