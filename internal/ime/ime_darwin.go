// Package ime turns the system input method off while single-key shortcuts are
// in use. A terminal hands keystrokes to the input method before the program
// sees them, so the only way to get them raw is to change the input source.
package ime

import (
	"log/slog"
	"runtime"

	"github.com/ebitengine/purego"
)

const carbonPath = "/System/Library/Frameworks/Carbon.framework/Carbon"

var (
	loaded, loadFailed bool
	copyCurrentSource  func() uintptr
	copyASCIISource    func() uintptr
	selectSource       func(source uintptr) int32
	cfRelease          func(ref uintptr)

	// saved is the source to go back to; non-zero while the input method is off.
	saved uintptr
)

// Text Input Source calls abort the process when made off the main thread.
// Locking here keeps the main goroutine, and so Bubble Tea's Update loop, on it.
func init() {
	runtime.LockOSThread()
}

func load() bool {
	if loaded || loadFailed {
		return loaded
	}
	lib, err := purego.Dlopen(carbonPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		slog.Warn("load Carbon", "err", err)
		loadFailed = true
		return false
	}
	purego.RegisterLibFunc(&copyCurrentSource, lib, "TISCopyCurrentKeyboardInputSource")
	purego.RegisterLibFunc(&copyASCIISource, lib, "TISCopyCurrentASCIICapableKeyboardLayoutInputSource")
	purego.RegisterLibFunc(&selectSource, lib, "TISSelectInputSource")
	purego.RegisterLibFunc(&cfRelease, lib, "CFRelease")
	loadTSM()
	loaded = true
	return true
}

// SetEnabled switches to a plain keyboard layout when enabled is false and back
// to the source that was active before when it is true. It must be called from
// the main goroutine.
func SetEnabled(enabled bool) {
	if !load() {
		return
	}
	if enabled {
		restore()
	} else {
		disable()
	}
}

func restore() {
	if saved == 0 {
		return
	}
	if !selectInFocusedApp(saved) {
		slog.Warn("focused app did not switch input source")
		if status := selectSource(saved); status != 0 {
			slog.Warn("restore input source", "status", status)
		}
	}
	cfRelease(saved)
	saved = 0
}

func disable() {
	if saved != 0 {
		return
	}
	current := copyCurrentSource()
	if current == 0 {
		return
	}
	ascii := copyASCIISource()
	if ascii == 0 {
		cfRelease(current)
		return
	}
	defer cfRelease(ascii)
	if status := selectSource(ascii); status != 0 {
		slog.Warn("select ASCII input source", "status", status)
		cfRelease(current)
		return
	}
	saved = current
}
