// Package config loads and saves the settings edited from inside the app.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const appName = "heiting"

// MaxVolume is the top of the volume scale; above 100 the audio is amplified.
const MaxVolume = 150

// Room is a saved live room.
type Room struct {
	ID   uint64 `toml:"id"`
	Name string `toml:"name"`
}

// Config is everything persisted between runs.
type Config struct {
	RoomID uint64 `toml:"room_id"`

	AutoPlay     bool `toml:"auto_play"`
	Volume       int  `toml:"volume"`
	Muted        bool `toml:"muted"`
	CacheSeconds int  `toml:"cache_seconds"`

	// LoginPrompted records that the first-run login prompt has been answered.
	LoginPrompted bool `toml:"login_prompted"`
	// Anonymous connects as a guest while keeping the saved login on disk.
	Anonymous bool `toml:"anonymous"`

	ChatBuffer     int  `toml:"chat_buffer"`
	ShowRoomTitle  bool `toml:"show_room_title"`
	ShowRoomNumber bool `toml:"show_room_number"`
	ColorMode      bool `toml:"color_mode"`
	ShowShipLevel  bool `toml:"show_ship_level"`
	ShowMedalName  bool `toml:"show_medal_name"`
	ShowMedalLevel bool `toml:"show_medal_level"`

	UserAgent string `toml:"user_agent"`

	Rooms []Room `toml:"rooms"`
}

// Default returns the settings used for anything missing from the file.
func Default() *Config {
	return &Config{
		AutoPlay:       true,
		Volume:         70,
		CacheSeconds:   3,
		ChatBuffer:     200,
		ShowRoomTitle:  true,
		ShowRoomNumber: true,
		ColorMode:      true,
		ShowShipLevel:  true,
		ShowMedalName:  true,
		ShowMedalLevel: true,
	}
}

// Dir is where the config and login cookie live.
func Dir() (string, error) {
	if dir := os.Getenv("HEITING_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
}

// CacheDir holds disposable files: the log and the login QR image.
func CacheDir() (string, error) {
	if dir := os.Getenv("HEITING_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, appName)
	return dir, os.MkdirAll(dir, 0o700)
}

// Load reads the config, returning defaults when none exists yet. A file that
// cannot be parsed is an error so that a later Save cannot overwrite it.
func Load() (*Config, error) {
	cfg := Default()
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.toml")
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}
	cfg.normalize()
	return cfg, nil
}

func (c *Config) normalize() {
	def := Default()
	c.Volume = clamp(c.Volume, 0, MaxVolume)
	if c.CacheSeconds <= 0 {
		c.CacheSeconds = def.CacheSeconds
	}
	if c.ChatBuffer <= 0 {
		c.ChatBuffer = def.ChatBuffer
	}
}

func clamp(v, low, high int) int {
	return min(max(v, low), high)
}

// Save writes the config atomically.
func (c *Config) Save() error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	return writeFile("config.toml", buf.Bytes())
}

// LoadCookie returns the saved login cookie, or "" when not logged in.
func LoadCookie() string {
	dir, err := Dir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "cookie"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveCookie stores the login cookie, readable only by the current user.
func SaveCookie(cookie string) error {
	return writeFile("cookie", []byte(cookie))
}

// DeleteCookie removes the saved login.
func DeleteCookie() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, "cookie"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func writeFile(name string, data []byte) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
