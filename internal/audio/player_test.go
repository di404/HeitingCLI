package audio

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestParseDevicesRemovesBackendDuplicates(t *testing.T) {
	output := `List of detected audio devices:
  'auto' (Autoselect device)
  'coreaudio/Speaker' (扬声器 (内建))
  'avfoundation/Speaker' (扬声器 (内建))
`
	devices := parseDevices(output)
	want := []Device{{"auto", "Autoselect device"}, {"avfoundation/Speaker", "扬声器 (内建)"}}
	if !slices.Equal(devices, want) {
		t.Fatalf("got %+v", devices)
	}
}

func TestBuildArgsDirectAndFallback(t *testing.T) {
	opts := Options{CacheSeconds: 3, Volume: 50}
	direct := buildArgs(Source{URL: "u", UserAgent: "ua", Referer: "r"}, opts)
	for _, want := range []string{"--vid=no", "--no-terminal", "--ytdl=no", "--user-agent=ua", "--referrer=r", "--volume=50", "--audio-device=auto"} {
		if !slices.Contains(direct, want) {
			t.Errorf("direct args missing %s", want)
		}
	}
	if !slices.Contains(buildArgs(Source{URL: "u", UseYTDL: true}, opts), "--ytdl=yes") {
		t.Error("fallback must enable yt-dlp")
	}
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
	p := NewPlayer(func(context.Context, uint64) (Source, error) {
		calls <- struct{}{}
		return Source{}, ErrOffline
	}, Options{})
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

func TestMissingMPVFailsWithoutRetry(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-mpv")
	p := NewPlayer(func(context.Context, uint64) (Source, error) {
		return Source{URL: "u"}, nil
	}, Options{MPVPath: missing})
	defer p.Close()
	p.Play(1)
	if status := waitForState(t, p, Failed); status.Err != errNoMPV.Error() {
		t.Fatalf("got %+v", status)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("test binary should not exist")
	}
}
