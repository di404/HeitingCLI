package audio

import (
	"context"
	"io"
	"sync"

	"github.com/ebitengine/oto/v3"
)

// sink is an open connection to the audio output.
type sink interface {
	SetVolume(volume float64)
	Err() error
	Close() error
}

// opener starts playing pcm, which is always 48 kHz stereo s16le.
type opener func(ctx context.Context, pcm io.Reader) (sink, error)

// deviceContext opens the system's default output. oto allows one context per
// process and cannot close it, so it lives until exit.
var deviceContext = sync.OnceValues(func() (*oto.Context, error) {
	device, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:      outputRate,
		ChannelCount:    2,
		Format:          oto.FormatSignedInt16LE,
		ApplicationName: "HeitingCLI",
	})
	if err != nil {
		return nil, err
	}
	<-ready
	return device, nil
})

type deviceTrack struct {
	*oto.Player
	device *oto.Context
}

func (t deviceTrack) Err() error { return t.device.Err() }

// Close releases the output so nothing keeps the device busy while stopped.
func (t deviceTrack) Close() error {
	err := t.Player.Close()
	_ = t.device.Suspend()
	return err
}

func openDevice(ctx context.Context, pcm io.Reader) (sink, error) {
	type result struct {
		device *oto.Context
		err    error
	}
	opened := make(chan result, 1)
	go func() {
		device, err := deviceContext()
		opened <- result{device, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-opened:
		if r.err != nil {
			return nil, r.err
		}
		if err := r.device.Resume(); err != nil {
			return nil, err
		}
		player := r.device.NewPlayer(pcm)
		player.Play()
		return deviceTrack{Player: player, device: r.device}, nil
	}
}
