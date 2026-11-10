package audio

import (
	"encoding/binary"
	"testing"
)

func frames(samples ...int16) []byte {
	var pcm []byte
	for _, sample := range samples {
		pcm = appendFrame(pcm, sample, sample)
	}
	return pcm
}

func readAll(q *pcmQueue, size int) []int16 {
	buf := make([]byte, size)
	n, _ := q.Read(buf)
	samples := make([]int16, n/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
	}
	return samples
}

func startedQueue() *pcmQueue {
	q := newPCMQueue(0)
	q.Write(frames(0))
	readAll(q, outputBytesPerFrame)
	return q
}

func TestUnityBoostKeepsSamplesExact(t *testing.T) {
	q := startedQueue()
	q.Write(frames(1234, -32768, 32767))
	got := readAll(q, 3*outputBytesPerFrame)
	want := []int16{1234, 1234, -32768, -32768, 32767, 32767}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
}

func TestBoostAmplifiesQuietSamples(t *testing.T) {
	q := startedQueue()
	q.setBoost(2)
	q.Write(frames(1000, 1000))
	readAll(q, 2*outputBytesPerFrame) // Ramps up to the new gain.
	q.Write(frames(1000))
	if got := readAll(q, outputBytesPerFrame); got[0] != 2000 {
		t.Fatalf("got %v", got)
	}
}

func TestBoostNeverWrapsAtFullScale(t *testing.T) {
	q := startedQueue()
	q.setBoost(3.375)
	q.Write(frames(0))
	readAll(q, outputBytesPerFrame)
	previous := int16(0)
	for sample := int32(0); sample <= 32767; sample += 97 {
		q.Write(frames(int16(sample)))
		got := readAll(q, outputBytesPerFrame)[0]
		if got < previous {
			t.Fatalf("sample %d: %d after %d", sample, got, previous)
		}
		previous = got
		q.Write(frames(int16(-sample - 1)))
		if negative := readAll(q, outputBytesPerFrame)[0]; negative > 0 {
			t.Fatalf("sample %d: %d", -sample-1, negative)
		}
	}
}

func TestLimitIsContinuousAtKnee(t *testing.T) {
	const delta = 1e-9
	if diff := limit(limiterKnee+delta) - (limiterKnee + delta); diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("jump %v", diff)
	}
	if limit(0.5) != 0.5 || limit(100) >= 1 || limit(-100) <= -1 {
		t.Fatal("limit out of shape")
	}
}
