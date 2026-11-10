package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("HEITING_CONFIG_DIR", t.TempDir())
	cfg := Default()
	cfg.RoomID = 7777
	cfg.Volume = 35
	cfg.ColorMode = false
	cfg.Rooms = []Room{{ID: 7777, Name: "主播"}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RoomID != 7777 || loaded.Volume != 35 || loaded.ColorMode || len(loaded.Rooms) != 1 || loaded.Rooms[0].Name != "主播" {
		t.Fatalf("got %+v", loaded)
	}
}

func TestMissingKeysKeepDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HEITING_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("room_id = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutoPlay || cfg.Volume != 70 || cfg.ChatBuffer != 200 {
		t.Fatalf("got %+v", cfg)
	}
}

func TestBrokenFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HEITING_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("room_id = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCookiePermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HEITING_CONFIG_DIR", dir)
	if err := SaveCookie("a=b"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "cookie"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if LoadCookie() != "a=b" {
		t.Fatal("cookie not read back")
	}
	if err := DeleteCookie(); err != nil {
		t.Fatal(err)
	}
	if LoadCookie() != "" {
		t.Fatal("cookie not deleted")
	}
}
