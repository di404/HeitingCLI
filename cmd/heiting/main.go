package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/di404/HeitingCLI/internal/audio"
	"github.com/di404/HeitingCLI/internal/bili"
	"github.com/di404/HeitingCLI/internal/config"
	"github.com/di404/HeitingCLI/internal/tui"
)

const maxLogSize = 5 << 20

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var initialRoom uint64
	if len(os.Args) > 2 {
		return fmt.Errorf("用法: %s [房间号或直播间链接]", filepath.Base(os.Args[0]))
	}
	if len(os.Args) == 2 {
		id, ok := bili.ParseRoomID(os.Args[1])
		if !ok {
			return fmt.Errorf("无效的房间号或直播间链接: %s", os.Args[1])
		}
		initialRoom = id
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if closeLog := setupLog(); closeLog != nil {
		defer closeLog()
	}

	cookies := ""
	if !cfg.Anonymous {
		cookies = config.LoadCookie()
	}
	client := bili.New(cfg.UserAgent, cookies)
	player := audio.NewPlayer(client.ResolveAudio, tui.AudioOptions(cfg))
	defer player.Close()

	model := tui.New(cfg, client, player, initialRoom)
	defer model.Close()

	_, err = tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

// setupLog sends logs to a file; anything on stderr would corrupt the screen.
func setupLog() func() {
	dir, err := config.CacheDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(dir, "heiting.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogSize {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(file, nil)))
	return func() { file.Close() }
}
