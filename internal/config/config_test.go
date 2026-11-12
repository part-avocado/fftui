package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/part-avocado/fftui/internal/config"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "nope", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg != config.Default() || cfg.Jobs != 1 {
		t.Errorf("got %+v, want defaults with jobs = 1", cfg)
	}
}

func TestLoadKeepsDefaultsForOmittedKeys(t *testing.T) {
	path := writeConfig(t, `
ascii = true
output_dir = 'C:\Users\Zoë\Videos'
ffmpeg_path = "/opt/ffmpeg 7/bin/ffmpeg"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := config.Config{
		Jobs:       1,
		ASCII:      true,
		OutputDir:  `C:\Users\Zoë\Videos`,
		FFmpegPath: "/opt/ffmpeg 7/bin/ffmpeg",
	}
	if cfg != want {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadReadsJobs(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, "jobs = 4\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Jobs != 4 {
		t.Errorf("jobs = %d, want 4", cfg.Jobs)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"zero jobs", "jobs = 0\n", "jobs must be at least 1"},
		{"negative jobs", "jobs = -2\n", "jobs must be at least 1"},
		{"misspelled key", "ascii = true\njob = 2\n", "job"},
		{"wrong type", `jobs = "two"` + "\n", "config.toml:1:"},
		{"invalid toml", "ascii = true\njobs = \n", "config.toml:2:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tt.content))
			if err == nil {
				t.Fatal("got no error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestSaveRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new folder", "config.toml")
	cfg := config.Config{
		Jobs:        3,
		FFmpegPath:  `C:\Program Files\FFmpeg "7"\bin\ffmpeg.exe`,
		FFprobePath: "/opt/ffmpeg/ffprobe",
		OutputDir:   "/home/zoë/Vidéos",
		ASCII:       true,
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg {
		t.Errorf("Load after Save = %+v, want %+v", got, cfg)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# How many FFmpeg jobs run at once\njobs = 3\n") {
		t.Errorf("settings are not explained:\n%s", data)
	}

	// Saving again replaces the file and leaves nothing else in the folder.
	cfg.Jobs = 1
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if got, _ := config.Load(path); got.Jobs != 1 {
		t.Errorf("jobs = %d after saving 1", got.Jobs)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("folder has %d files, want only config.toml", len(entries))
	}
}

func TestSaveRefusesInvalidSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.Config{Jobs: 0}); err == nil || !strings.Contains(err.Error(), "jobs must be at least 1") {
		t.Errorf("Save = %v, want a jobs error", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("an invalid config was written")
	}
}
