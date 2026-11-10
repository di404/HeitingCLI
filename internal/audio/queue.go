package audio

import "sync"

// driftSeconds is how far the queue may grow past its target before it is cut
// back, so delay cannot build up on a live stream.
const driftSeconds = 2

// pcmQueue sits between the network and the audio device. Read never blocks
// because the device's mixer calls it while holding its own locks; when there
// is nothing to play it returns silence.
type pcmQueue struct {
	mu      sync.Mutex
	data    []byte
	head    int
	target  int
	started bool
	boost   float64
	applied float64
}

func newPCMQueue(seconds int) *pcmQueue {
	q := &pcmQueue{boost: 1, applied: 1}
	q.setTarget(seconds)
	return q
}

func (q *pcmQueue) setTarget(seconds int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.target = seconds * outputBytesPerSecond
}

// setBoost sets the amplification; 1 leaves the samples untouched.
func (q *pcmQueue) setBoost(boost float64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.boost = boost
}

func (q *pcmQueue) Write(p []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.head > 0 && len(q.data)+len(p) > cap(q.data) {
		q.data = q.data[:copy(q.data, q.data[q.head:])]
		q.head = 0
	}
	q.data = append(q.data, p...)
	queued := len(q.data) - q.head
	if queued >= q.target {
		q.started = true
	}
	if queued > q.target+driftSeconds*outputBytesPerSecond {
		q.head += (queued - q.target) &^ (outputBytesPerFrame - 1)
	}
}

func (q *pcmQueue) Read(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	size := len(p) &^ (outputBytesPerFrame - 1)
	if q.started && q.head == len(q.data) {
		q.started = false
	}
	if !q.started {
		clear(p[:size])
		q.applied = q.boost
		return size, nil
	}
	n := copy(p[:size], q.data[q.head:])
	q.head += n
	amplify(p[:n], q.applied, q.boost)
	q.applied = q.boost
	return n, nil
}

func (q *pcmQueue) playing() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.started
}
