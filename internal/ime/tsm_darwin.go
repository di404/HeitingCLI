package ime

import (
	"log/slog"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// TISSelectInputSource from a process without keyboard focus commits the new
// source system-wide before telling the focused app. A third-party input method
// is then activated and dropped a moment later: the menu bar shows it while
// typing stays Latin. Selecting from the Input menu does not have this problem
// because it asks the focused app to switch through its Text Services Manager
// port, so selectInFocusedApp does the same.
//
// These symbols are private, so all of them are optional and callers fall back
// to TISSelectInputSource. Technique from https://github.com/laishulu/macism.

const (
	appKitPath = "/System/Library/Frameworks/AppKit.framework/AppKit"

	tsmPortName        = "com.apple.tsm.portname"
	tsmSelectSourceKey = "tsmInputSourceSelectedInpSrcKey"
	tsmReplyErrorKey   = "tsmInputModeReplyToServerErrorKey"
	tsmSelectSourceMsg = 7
	tsmTimeoutSeconds  = 0.3

	kCFStringEncodingUTF8      = 0x08000100
	kCFNumberSInt32Type        = 3
	kCFPropertyListXMLFormat10 = 100
	nsActivationProhibited     = 2
)

var (
	tsmLoaded bool

	cpsGetKeyFocusProcess func(psn *[2]uint32, ignored *uint8) int32
	getProcessPID         func(psn *[2]uint32, pid *int32) int32
	createFlattenedSource func(source uintptr, flat *uintptr) bool

	cfRemotePort       func(alloc, name uintptr, pid int32) uintptr
	cfSendRequest      func(port uintptr, id int32, data uintptr, send, recv float64, mode uintptr, reply *uintptr) int32
	cfStringCreate     func(alloc uintptr, text string, encoding uint32) uintptr
	cfDictionaryCreate func(alloc uintptr, keys, values *uintptr, count int, keyCallbacks, valueCallbacks uintptr) uintptr
	cfDictionaryGet    func(dict, key uintptr) uintptr
	cfDictionaryType   func() uintptr
	cfNumberType       func() uintptr
	cfNumberGet        func(number uintptr, kind int, value *int32) bool
	cfGetType          func(ref uintptr) uintptr
	cfPlistToData      func(alloc, plist uintptr, format, options int, err uintptr) uintptr
	cfPlistFromData    func(alloc, data uintptr, options int, format, err uintptr) uintptr

	cfKeyCallbacks, cfValueCallbacks uintptr
)

func bind(fn any, name string) bool {
	addr, err := purego.Dlsym(purego.RTLD_DEFAULT, name)
	if err != nil || addr == 0 {
		slog.Warn("input method symbol missing", "name", name)
		return false
	}
	purego.RegisterFunc(fn, addr)
	return true
}

func symbol(name string) uintptr {
	addr, err := purego.Dlsym(purego.RTLD_DEFAULT, name)
	if err != nil {
		slog.Warn("input method symbol missing", "name", name)
		return 0
	}
	return addr
}

func loadTSM() {
	if _, err := purego.Dlopen(appKitPath, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		slog.Warn("load AppKit", "err", err)
		return
	}
	bound := bind(&cpsGetKeyFocusProcess, "CPSGetKeyFocusProcess") &&
		bind(&getProcessPID, "GetProcessPID") &&
		bind(&createFlattenedSource, "_CreateFlattenedInputSource") &&
		bind(&cfRemotePort, "CFMessagePortCreatePerProcessRemote") &&
		bind(&cfSendRequest, "CFMessagePortSendRequest") &&
		bind(&cfStringCreate, "CFStringCreateWithCString") &&
		bind(&cfDictionaryCreate, "CFDictionaryCreate") &&
		bind(&cfDictionaryGet, "CFDictionaryGetValue") &&
		bind(&cfDictionaryType, "CFDictionaryGetTypeID") &&
		bind(&cfNumberType, "CFNumberGetTypeID") &&
		bind(&cfNumberGet, "CFNumberGetValue") &&
		bind(&cfGetType, "CFGetTypeID") &&
		bind(&cfPlistToData, "CFPropertyListCreateData") &&
		bind(&cfPlistFromData, "CFPropertyListCreateWithData")
	cfKeyCallbacks = symbol("kCFTypeDictionaryKeyCallBacks")
	cfValueCallbacks = symbol("kCFTypeDictionaryValueCallBacks")
	if !bound || cfKeyCallbacks == 0 || cfValueCallbacks == 0 {
		return
	}

	// CPSGetKeyFocusProcess needs a connection to the window server.
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	if app == 0 {
		return
	}
	app.Send(objc.RegisterName("setActivationPolicy:"), nsActivationProhibited)
	tsmLoaded = true
}

func cfString(text string) uintptr {
	return cfStringCreate(0, text, kCFStringEncodingUTF8)
}

func keyFocusPID() (int32, bool) {
	var psn [2]uint32
	var ignored uint8
	var pid int32 = -1
	if cpsGetKeyFocusProcess(&psn, &ignored) != 0 || getProcessPID(&psn, &pid) != 0 || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// selectInFocusedApp reports whether the focused app accepted the switch. The
// request is synchronous, so keys typed next already reach the input method.
func selectInFocusedApp(source uintptr) bool {
	if !tsmLoaded {
		return false
	}
	pid, ok := keyFocusPID()
	if !ok {
		return false
	}

	var flat uintptr
	if !createFlattenedSource(source, &flat) || flat == 0 {
		return false
	}
	defer cfRelease(flat)

	key := cfString(tsmSelectSourceKey)
	defer cfRelease(key)
	request := cfDictionaryCreate(0, &key, &flat, 1, cfKeyCallbacks, cfValueCallbacks)
	if request == 0 {
		return false
	}
	defer cfRelease(request)
	data := cfPlistToData(0, request, kCFPropertyListXMLFormat10, 0, 0)
	if data == 0 {
		return false
	}
	defer cfRelease(data)

	name := cfString(tsmPortName)
	defer cfRelease(name)
	port := cfRemotePort(0, name, pid)
	if port == 0 {
		return false
	}
	defer cfRelease(port)

	// Run loop modes are compared by value, so this equals kCFRunLoopDefaultMode.
	mode := cfString("kCFRunLoopDefaultMode")
	defer cfRelease(mode)

	var reply uintptr
	if cfSendRequest(port, tsmSelectSourceMsg, data, tsmTimeoutSeconds, tsmTimeoutSeconds, mode, &reply) != 0 {
		return false
	}
	if reply == 0 {
		return true
	}
	defer cfRelease(reply)
	return replySucceeded(reply)
}

func replySucceeded(reply uintptr) bool {
	plist := cfPlistFromData(0, reply, 0, 0, 0)
	if plist == 0 {
		return true
	}
	defer cfRelease(plist)
	if cfGetType(plist) != cfDictionaryType() {
		return true
	}
	key := cfString(tsmReplyErrorKey)
	defer cfRelease(key)
	value := cfDictionaryGet(plist, key)
	if value == 0 || cfGetType(value) != cfNumberType() {
		return true
	}
	var code int32
	return !cfNumberGet(value, kCFNumberSInt32Type, &code) || code == 0
}
