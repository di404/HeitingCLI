//go:build windows

package audio

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/Microsoft/go-winio"
)

var pipeCounter atomic.Uint64

// ipcEndpoint connects to mpv over a named pipe; mpv on Windows does not
// support inheriting a socket.
type ipcEndpoint struct {
	path string
}

func newIPCEndpoint() (*ipcEndpoint, error) {
	return &ipcEndpoint{path: fmt.Sprintf(`\\.\pipe\heiting-%d-%d`, os.Getpid(), pipeCounter.Add(1))}, nil
}

func (e *ipcEndpoint) args() []string {
	return []string{"--input-ipc-server=" + e.path}
}

func (e *ipcEndpoint) prepare(*exec.Cmd) {}

func (e *ipcEndpoint) started() {}

func (e *ipcEndpoint) connect(ctx context.Context) (net.Conn, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		timeout := 200 * time.Millisecond
		conn, err := winio.DialPipe(e.path, &timeout)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (e *ipcEndpoint) close() {}
