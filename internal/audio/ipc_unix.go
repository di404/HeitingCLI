//go:build !windows

package audio

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"syscall"
)

// ipcEndpoint connects to mpv over an inherited socketpair. When this process
// dies for any reason, even SIGKILL, the socket closes and mpv quits, so
// playback can never be orphaned.
type ipcEndpoint struct {
	parent *os.File
	child  *os.File
}

func newIPCEndpoint() (*ipcEndpoint, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	return &ipcEndpoint{
		parent: os.NewFile(uintptr(fds[0]), "mpv-ipc"),
		child:  os.NewFile(uintptr(fds[1]), "mpv-ipc-child"),
	}, nil
}

// args uses fd 3: the first entry of cmd.ExtraFiles.
func (e *ipcEndpoint) args() []string {
	return []string{"--input-ipc-client=fd://3"}
}

func (e *ipcEndpoint) prepare(cmd *exec.Cmd) {
	cmd.ExtraFiles = []*os.File{e.child}
}

// started drops our copy of the child's end so that EOF is seen when mpv exits.
func (e *ipcEndpoint) started() {
	e.child.Close()
	e.child = nil
}

func (e *ipcEndpoint) connect(context.Context) (net.Conn, error) {
	if e.parent == nil {
		return nil, errors.New("ipc endpoint already used")
	}
	conn, err := net.FileConn(e.parent)
	e.parent.Close()
	e.parent = nil
	return conn, err
}

func (e *ipcEndpoint) close() {
	if e.parent != nil {
		e.parent.Close()
	}
	if e.child != nil {
		e.child.Close()
	}
}
