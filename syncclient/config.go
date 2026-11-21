// Package syncclient syncs the local store with the postitd service.
package syncclient

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Config says where the server is and how to authenticate.
type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

// Enabled reports whether sync is configured.
func (c Config) Enabled() bool { return c.Server != "" && c.Token != "" }

// ConfigPath is $XDG_CONFIG_HOME/postit/sync.json, default ~/.config/postit/sync.json.
func ConfigPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "postit", "sync.json"), nil
}

// LoadConfig reads the config file; POSTIT_SERVER and POSTIT_TOKEN override
// it. A missing file is not an error, sync is just off.
func LoadConfig() (Config, error) {
	var c Config
	path, err := ConfigPath()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return c, err
	default:
		if err := json.Unmarshal(b, &c); err != nil {
			return c, err
		}
	}
	if v := os.Getenv("POSTIT_SERVER"); v != "" {
		c.Server = v
	}
	if v := os.Getenv("POSTIT_TOKEN"); v != "" {
		c.Token = v
	}
	c.Server = strings.TrimRight(c.Server, "/")
	return c, nil
}

// SaveConfig writes c to the config file, readable only by the user.
func SaveConfig(c Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
