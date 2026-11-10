// Package audio plays a live room's audio-only FLV stream inside the process:
// HTTP, FLV demuxing, AAC decoding and output to the system's default device.
package audio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

var (
	// ErrOffline is returned by a Resolver when the room is not broadcasting.
	ErrOffline = errors.New("直播未开始")
	// ErrNoStream is returned by a Resolver when the room is live but offers
	// no stream this player can use.
	ErrNoStream = errors.New("没有可用的音频流")
)

// fatalError stops the retry loop: reconnecting cannot help.
type fatalError struct {
	msg    string
	detail string
}

func (e fatalError) Error() string { return e.msg }

// Source lists the addresses of one stream, best first.
type Source struct {
	URLs      []string
	UserAgent string
	Referer   string
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
	CacheSeconds int
	Volume       int
	Muted        bool
}

// gain follows the same cubic curve as mpv, which matches perceived loudness
// far better than a linear scale.
func (o Options) gain() float64 {
	if o.Muted {
		return 0
	}
	v := float64(o.Volume) / 100
	return v * v * v
}

// Player plays one stream at a time.
type Player struct {
	resolve Resolver
	open    opener
	client  *http.Client
	updates chan Status
	wake    chan struct{}

	mu     sync.Mutex
	opts   Options
	status Status
	room   uint64
	cancel context.CancelFunc
	done   chan struct{}
	track  sink
	queue  *pcmQueue
}

// NewPlayer creates a stopped player. The audio device is opened on first use.
func NewPlayer(resolve Resolver, opts Options) *Player {
	return &Player{
		resolve: resolve,
		open:    openDevice,
		client:  &http.Client{},
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

// Close stops playback and waits for it to finish.
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

// Configure applies new options to the current playback.
func (p *Player) Configure(opts Options) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opts = opts
	if p.queue != nil {
		p.applyOptionsLocked()
	}
}

// applyOptionsLocked leaves attenuation to the device, whose mixer has no
// limiter, and amplifies in the queue, where peaks can still be contained.
func (p *Player) applyOptionsLocked() {
	gain := p.opts.gain()
	p.track.SetVolume(min(gain, 1))
	p.queue.setBoost(max(gain, 1))
	p.queue.setTarget(p.opts.CacheSeconds)
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
		case errors.Is(err, ErrNoStream):
			p.setStatus(ctx, Status{State: Failed, Err: err.Error()})
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
		var fatal fatalError
		if errors.As(err, &fatal) {
			slog.Error("playback failed", "room", roomID, "err", err, "detail", fatal.detail)
			p.setStatus(ctx, Status{State: Failed, Err: fatal.msg})
			return
		}
		slog.Warn("playback interrupted", "room", roomID, "err", err)
		if time.Since(started) > stableAfter {
			backoff = minBackoff
		}
		p.setStatus(ctx, Status{State: Retrying})
		p.wait(ctx, backoff)
		backoff = min(backoff*2, maxBackoff)
	}
}

// attach publishes the running output to Configure.
func (p *Player) attach(track sink, queue *pcmQueue) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.track, p.queue = track, queue
	p.applyOptionsLocked()
}

func (p *Player) detach() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.track, p.queue = nil, nil
}

func (p *Player) playOnce(ctx context.Context, src Source) error {
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	queue := newPCMQueue(1)
	track, err := p.open(ctx, queue)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fatalError{msg: "无法打开音频设备", detail: err.Error()}
	}
	defer track.Close()
	p.attach(track, queue)
	defer p.detach()

	conn, flv, err := p.connect(ctx, src)
	if err != nil {
		return err
	}
	defer conn.Close()

	watched := make(chan struct{})
	watchCtx, stopWatch := context.WithCancel(ctx)
	go func() {
		defer close(watched)
		p.watch(watchCtx, ctx, queue, track, stop)
	}()
	defer func() {
		stopWatch()
		<-watched
	}()

	err = decodeStream(flv, queue.Write)
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return err
}

// watch reports buffering changes and stops playback if the device fails.
func (p *Player) watch(ctx, statusCtx context.Context, queue *pcmQueue, track sink, stop context.CancelCauseFunc) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	current := Buffering
	p.setStatus(statusCtx, Status{State: current})
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := track.Err(); err != nil {
			stop(fatalError{msg: "音频设备出错", detail: err.Error()})
			return
		}
		next := Buffering
		if queue.playing() {
			next = Playing
		}
		if next != current {
			current = next
			p.setStatus(statusCtx, Status{State: current})
		}
	}
}

// connect opens the first address that delivers an FLV stream.
func (p *Player) connect(ctx context.Context, src Source) (*connection, *flvSource, error) {
	err := error(ErrNoStream)
	for _, url := range src.URLs {
		var conn *connection
		conn, err = p.dial(ctx, url, src)
		if err != nil {
			slog.Warn("connect stream", "err", err)
			continue
		}
		var flv *flvSource
		flv, err = newFLVSource(conn)
		if err != nil {
			slog.Warn("open stream", "err", err)
			conn.Close()
			continue
		}
		return conn, flv, nil
	}
	return nil, nil, err
}

const stallTimeout = 10 * time.Second

var errStalled = errors.New("音频流超时")

// connection is an HTTP stream that fails when no data arrives for
// stallTimeout, which a plain read would never notice on a half-dead socket.
type connection struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  *time.Timer
	body   interface {
		Read([]byte) (int, error)
		Close() error
	}
}

func (p *Player) dial(parent context.Context, url string, src Source) (*connection, error) {
	ctx, cancel := context.WithCancelCause(parent)
	timer := time.AfterFunc(stallTimeout, func() { cancel(errStalled) })
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		timer.Stop()
		cancel(nil)
		return nil, err
	}
	req.Header.Set("User-Agent", src.UserAgent)
	req.Header.Set("Referer", src.Referer)
	resp, err := p.client.Do(req)
	if err != nil {
		timer.Stop()
		cancel(nil)
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		timer.Stop()
		cancel(nil)
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return &connection{ctx: ctx, cancel: cancel, timer: timer, body: resp.Body}, nil
}

func (c *connection) Read(b []byte) (int, error) {
	n, err := c.body.Read(b)
	if n > 0 {
		c.timer.Reset(stallTimeout)
	}
	if err != nil && c.ctx.Err() != nil {
		err = context.Cause(c.ctx)
	}
	return n, err
}

func (c *connection) Close() error {
	c.timer.Stop()
	c.cancel(nil)
	return c.body.Close()
}
