package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	aac "github.com/tphakala/go-aac"
)

func flvHeader() []byte {
	return []byte{'F', 'L', 'V', 1, 4, 0, 0, 0, 9, 0, 0, 0, 0}
}

func appendTag(dst []byte, kind byte, data []byte) []byte {
	size := len(data)
	dst = append(dst, kind, byte(size>>16), byte(size>>8), byte(size), 0, 0, 0, 0, 0, 0, 0)
	dst = append(dst, data...)
	return binary.BigEndian.AppendUint32(dst, uint32(size+11))
}

func appendAAC(dst []byte, packet byte, data []byte) []byte {
	return appendTag(dst, flvTagAudio, append([]byte{0xaf, packet}, data...))
}

// toneFLV encodes a stereo sine wave into an FLV with video tags mixed in.
func toneFLV(t *testing.T, rate int, seconds float64) []byte {
	t.Helper()
	encoder, err := aac.NewEncoder(aac.EncoderConfig{SampleRate: rate, Channels: 2, Bitrate: 128000})
	if err != nil {
		t.Fatal(err)
	}
	stream := appendAAC(flvHeader(), aacPacketASC, encoder.AudioSpecificConfig())
	samples := make([]float32, aac.FrameSize)
	frames := int(seconds * float64(rate) / aac.FrameSize)
	for f := range frames {
		for i := range samples {
			n := f*aac.FrameSize + i
			samples[i] = float32(0.5 * math.Sin(2*math.Pi*1000*float64(n)/float64(rate)))
		}
		unit, err := encoder.EncodeFrame(nil, [][]float32{samples, samples})
		if err != nil {
			t.Fatal(err)
		}
		if len(unit) > 0 {
			stream = appendAAC(stream, aacPacketRaw, unit)
		}
		stream = appendTag(stream, 9, []byte{0x17, 1, 0, 0, 0})
	}
	return stream
}

func decodeAll(t *testing.T, stream []byte) []byte {
	t.Helper()
	src, err := newFLVSource(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	if err := decodeStream(src, func(p []byte) { out = append(out, p...) }); err != errStreamEnded {
		t.Fatalf("got %v", err)
	}
	return out
}

func peak(pcm []byte) int {
	best := 0
	for i := 0; i+2 <= len(pcm); i += 2 {
		best = max(best, int(int16(binary.LittleEndian.Uint16(pcm[i:]))))
	}
	return best
}

func TestDecodesAudioAndIgnoresVideo(t *testing.T) {
	out := decodeAll(t, toneFLV(t, outputRate, 1))
	if seconds := float64(len(out)) / outputBytesPerSecond; seconds < 0.9 || seconds > 1.1 {
		t.Fatalf("decoded %.2fs", seconds)
	}
	if p := peak(out[len(out)/2:]); p < 12000 || p > 20000 {
		t.Fatalf("peak %d", p)
	}
}

func TestOtherSampleRateIsConverted(t *testing.T) {
	out := decodeAll(t, toneFLV(t, 44100, 1))
	if seconds := float64(len(out)) / outputBytesPerSecond; seconds < 0.9 || seconds > 1.1 {
		t.Fatalf("decoded %.2fs", seconds)
	}
}

func TestConfigChangeRebuildsDecoder(t *testing.T) {
	first := toneFLV(t, outputRate, 0.5)
	second := toneFLV(t, 44100, 0.5)
	out := decodeAll(t, append(first, second[len(flvHeader()):]...))
	if seconds := float64(len(out)) / outputBytesPerSecond; seconds < 0.9 || seconds > 1.1 {
		t.Fatalf("decoded %.2fs", seconds)
	}
}

func TestRejectsNonFLV(t *testing.T) {
	if _, err := newFLVSource(bytes.NewReader([]byte("<html>not a stream</html>"))); err == nil {
		t.Fatal("expected an error")
	}
}

func TestConverterKeepsRateAcrossChunks(t *testing.T) {
	c := newConverter(44100, 1)
	var out []byte
	chunk := make([]byte, 882)
	for range 100 {
		out = c.append(out, chunk)
	}
	want := 44100.0 * outputRate / 44100 * outputBytesPerFrame
	if got := float64(len(out)); math.Abs(got-want) > 64 {
		t.Fatalf("got %.0f bytes, want about %.0f", got, want)
	}
}

func TestMonoIsDuplicated(t *testing.T) {
	c := newConverter(outputRate, 1)
	got := c.append(nil, []byte{1, 0, 2, 0})
	if !bytes.Equal(got, []byte{1, 0, 1, 0, 2, 0, 2, 0}) {
		t.Fatalf("got %v", got)
	}
}

func TestQueueWaitsForTargetThenPlays(t *testing.T) {
	q := newPCMQueue(1)
	block := bytes.Repeat([]byte{1}, outputBytesPerSecond/2)
	read := make([]byte, 400)

	q.Write(block)
	if n, _ := q.Read(read); n != 400 || read[0] != 0 || q.playing() {
		t.Fatalf("expected silence while buffering, got %d %d", n, read[0])
	}
	q.Write(block)
	if n, _ := q.Read(read); n != 400 || read[0] != 1 || !q.playing() {
		t.Fatalf("expected audio, got %d %d", n, read[0])
	}
}

func TestQueueRebuffersAfterUnderrun(t *testing.T) {
	q := newPCMQueue(1)
	q.Write(make([]byte, outputBytesPerSecond))
	buf := make([]byte, outputBytesPerSecond)
	q.Read(buf)
	if n, _ := q.Read(buf); n != len(buf) || q.playing() {
		t.Fatalf("got %d playing=%v", n, q.playing())
	}
}

func TestQueueDropsBacklog(t *testing.T) {
	q := newPCMQueue(1)
	q.Write(make([]byte, outputBytesPerSecond*5))
	if queued := len(q.data) - q.head; queued != outputBytesPerSecond {
		t.Fatalf("queued %d", queued)
	}
}

func TestGainIsCubicAndMutes(t *testing.T) {
	if g := (Options{Volume: 50}).gain(); g != 0.125 {
		t.Fatalf("got %v", g)
	}
	if g := (Options{Volume: 100, Muted: true}).gain(); g != 0 {
		t.Fatalf("got %v", g)
	}
}

type fakeSink struct {
	mu     sync.Mutex
	pcm    io.Reader
	volume float64
	err    error
	closed bool
}

func (s *fakeSink) SetVolume(v float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volume = v
}

func (s *fakeSink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *fakeSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeSink) getVolume() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.volume
}

func newTestPlayer(resolve Resolver, output *fakeSink, opts Options) *Player {
	p := NewPlayer(resolve, opts)
	p.open = func(_ context.Context, pcm io.Reader) (sink, error) {
		output.pcm = pcm
		return output, nil
	}
	return p
}

func waitForState(t *testing.T, p *Player, want State) Status {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case status := <-p.Updates():
			if status.State == want {
				return status
			}
		case <-deadline:
			t.Fatalf("timed out waiting for state %d, last %+v", want, p.Status())
		}
	}
}

func TestOfflineThenWake(t *testing.T) {
	calls := make(chan struct{}, 8)
	p := newTestPlayer(func(context.Context, uint64) (Source, error) {
		calls <- struct{}{}
		return Source{}, ErrOffline
	}, new(fakeSink), Options{})
	defer p.Close()
	p.Play(1)
	waitForState(t, p, Offline)
	<-calls
	p.Wake()
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("Wake did not trigger another resolve")
	}
}

func TestPlaysFromSecondAddressAndHoldsConnection(t *testing.T) {
	stream := toneFLV(t, outputRate, 2)
	release := make(chan struct{})
	defer close(release)
	var gotUA, gotReferer string
	var seen sync.Mutex
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Lock()
		gotUA, gotReferer = r.UserAgent(), r.Referer()
		seen.Unlock()
		w.Write(stream)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer good.Close()
	bad := httptest.NewServer(http.NotFoundHandler())
	defer bad.Close()

	output := new(fakeSink)
	p := newTestPlayer(func(context.Context, uint64) (Source, error) {
		return Source{URLs: []string{bad.URL, good.URL}, UserAgent: "ua", Referer: "ref"}, nil
	}, output, Options{CacheSeconds: 1, Volume: 100})
	defer p.Close()
	p.Play(1)
	waitForState(t, p, Playing)

	buf := make([]byte, 96000)
	if n, _ := output.pcm.Read(buf); peak(buf[:n]) < 12000 {
		t.Fatal("expected decoded audio")
	}
	if output.getVolume() != 1 {
		t.Fatalf("volume %v", output.getVolume())
	}
	p.Configure(Options{CacheSeconds: 1, Volume: 150})
	if output.getVolume() != 1 {
		t.Fatalf("device must not amplify: volume %v", output.getVolume())
	}
	p.Configure(Options{CacheSeconds: 1, Volume: 100, Muted: true})
	if output.getVolume() != 0 {
		t.Fatal("mute was not applied")
	}
	seen.Lock()
	defer seen.Unlock()
	if gotUA != "ua" || gotReferer != "ref" {
		t.Fatalf("headers %q %q", gotUA, gotReferer)
	}
}

func TestEndedStreamReconnects(t *testing.T) {
	stream := toneFLV(t, outputRate, 0.2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(stream)
	}))
	defer server.Close()
	p := newTestPlayer(func(context.Context, uint64) (Source, error) {
		return Source{URLs: []string{server.URL}}, nil
	}, new(fakeSink), Options{CacheSeconds: 1})
	defer p.Close()
	p.Play(1)
	waitForState(t, p, Retrying)
}

func TestUnsupportedCodecFailsWithoutRetry(t *testing.T) {
	stream := appendTag(flvHeader(), flvTagAudio, []byte{0x2f, 0, 1, 2, 3})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(stream)
	}))
	defer server.Close()
	p := newTestPlayer(func(context.Context, uint64) (Source, error) {
		return Source{URLs: []string{server.URL}}, nil
	}, new(fakeSink), Options{CacheSeconds: 1})
	defer p.Close()
	p.Play(1)
	if status := waitForState(t, p, Failed); status.Err != "不支持的音频格式" {
		t.Fatalf("got %+v", status)
	}
}

func TestNoDeviceFailsWithoutRetry(t *testing.T) {
	p := NewPlayer(func(context.Context, uint64) (Source, error) {
		return Source{URLs: []string{"http://127.0.0.1:1"}}, nil
	}, Options{CacheSeconds: 1})
	p.open = func(context.Context, io.Reader) (sink, error) {
		return nil, io.ErrClosedPipe
	}
	defer p.Close()
	p.Play(1)
	if status := waitForState(t, p, Failed); status.Err != "无法打开音频设备" {
		t.Fatalf("got %+v", status)
	}
}
