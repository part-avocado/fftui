// Package config loads and saves fftui's settings in config.toml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Config holds the user's settings. Every field has a usable zero or default
// value, so a missing config file is not an error.
type Config struct {
	// Jobs is how many FFmpeg jobs run at once. It must be at least 1.
	Jobs int `toml:"jobs" comment:"How many FFmpeg jobs run at once"`
	// FFmpegPath and FFprobePath override binary discovery when set.
	FFmpegPath  string `toml:"ffmpeg_path" comment:"Custom ffmpeg program; empty finds it automatically"`
	FFprobePath string `toml:"ffprobe_path" comment:"Custom ffprobe program; empty uses the one next to ffmpeg"`
	// OutputDir is the default output folder. Empty means next to the input.
	OutputDir string `toml:"output_dir" comment:"Default output folder; empty means next to the input"`
	// ASCII swaps box-drawing characters and symbols for plain ASCII.
	ASCII bool `toml:"ascii" comment:"Draw with plain ASCII, for limited terminals or fonts"`
}

// Default returns the settings used when the config file omits a value.
func Default() Config {
	return Config{Jobs: 1}
}

// DefaultPath returns config.toml in fftui's folder under the OS's user
// config directory, such as ~/.config/fftui on Linux,
// ~/Library/Application Support/fftui on macOS, and %AppData%\fftui on Windows.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "fftui", "config.toml"), nil
}

// Load reads the config file at path. A missing file gives Default(), and
// keys the file omits keep their default values. Unknown keys are an error so
// that a misspelled setting is reported instead of silently ignored.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}

	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return cfg, fmt.Errorf("%s: unknown setting:\n%s", path, strict.String())
		}
		var decErr *toml.DecodeError
		if errors.As(err, &decErr) {
			row, col := decErr.Position()
			return cfg, fmt.Errorf("%s:%d:%d: %s", path, row, col, decErr.Error())
		}
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Validate reports a setting that cannot work.
func (c Config) Validate() error {
	if c.Jobs < 1 {
		return fmt.Errorf("jobs must be at least 1, got %d", c.Jobs)
	}
	return nil
}

// header starts every config file Save writes.
const header = "# fftui settings. fftui rewrites this file when you change settings in\n# the app, so comments added here are not kept.\n\n"

// Save writes cfg to path, creating its folder. It replaces the file in one
// step, so a crash cannot leave half a file behind.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // fails harmlessly once renamed
	_, err = f.WriteString(header + string(data))
	if err == nil {
		err = f.Chmod(0o644)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
