package ffmpeg_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

// exe creates an empty executable named name in dir and returns its path. On Windows it gets the .exe that exec.LookPath requires.
func exe(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocate(t *testing.T) {
	root := t.TempDir()
	onPath := filepath.Join(root, "on path")
	pathFFmpeg := exe(t, onPath, "ffmpeg")
	pathFFprobe := exe(t, onPath, "ffprobe")
	custom := filepath.Join(root, "custom build")
	customFFmpeg := exe(t, custom, "ffmpeg")
	customFFprobe := exe(t, custom, "ffprobe")
	lonely := exe(t, filepath.Join(root, "lonely"), "ffmpeg7")
	brew := filepath.Join(root, "brew")
	brewFFmpeg := exe(t, brew, "ffmpeg")
	brewFFprobe := exe(t, brew, "ffprobe")
	home := filepath.Join(root, "home")
	homeFFmpeg := exe(t, filepath.Join(home, "bin"), "ffmpeg")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	tests := []struct {
		name   string
		path   string // PATH for the test
		search ffmpeg.Search
		want   ffmpeg.Binaries
	}{
		{
			name: "PATH",
			path: onPath,
			want: ffmpeg.Binaries{FFmpeg: pathFFmpeg, FFprobe: pathFFprobe, Source: "PATH"},
		},
		{
			name:   "PATH comes before the usual folders",
			path:   onPath,
			search: ffmpeg.Search{Dirs: []string{brew}},
			want:   ffmpeg.Binaries{FFmpeg: pathFFmpeg, FFprobe: pathFFprobe, Source: "PATH"},
		},
		{
			name:   "chosen ffmpeg uses the ffprobe next to it, not the one on PATH",
			path:   onPath,
			search: ffmpeg.Search{FFmpeg: customFFmpeg, Source: "--ffmpeg"},
			want:   ffmpeg.Binaries{FFmpeg: customFFmpeg, FFprobe: customFFprobe, Source: "--ffmpeg"},
		},
		{
			name:   "chosen ffmpeg with no ffprobe next to it falls back to PATH",
			path:   onPath,
			search: ffmpeg.Search{FFmpeg: lonely, Source: "FFTUI_FFMPEG"},
			want:   ffmpeg.Binaries{FFmpeg: lonely, FFprobe: pathFFprobe, Source: "FFTUI_FFMPEG"},
		},
		{
			name:   "chosen ffprobe wins over the one next to ffmpeg",
			path:   onPath,
			search: ffmpeg.Search{FFmpeg: customFFmpeg, Source: "--ffmpeg", FFprobe: pathFFprobe},
			want:   ffmpeg.Binaries{FFmpeg: customFFmpeg, FFprobe: pathFFprobe, Source: "--ffmpeg"},
		},
		{
			name:   "home folder in a chosen path",
			path:   onPath,
			search: ffmpeg.Search{FFmpeg: "~/bin/ffmpeg", Source: "config ffmpeg_path"},
			want:   ffmpeg.Binaries{FFmpeg: homeFFmpeg, FFprobe: pathFFprobe, Source: "config ffmpeg_path"},
		},
		{
			name:   "usual folder when PATH has nothing",
			path:   filepath.Join(root, "empty"),
			search: ffmpeg.Search{Dirs: []string{filepath.Join(root, "nope"), brew}},
			want:   ffmpeg.Binaries{FFmpeg: brewFFmpeg, FFprobe: brewFFprobe, Source: brew},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", tt.path)
			got, err := ffmpeg.Locate(tt.search)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestLocateNotFound(t *testing.T) {
	root := t.TempDir()
	onPath := filepath.Join(root, "bin")
	exe(t, onPath, "ffmpeg")
	onlyFFmpeg := filepath.Join(root, "only ffmpeg")
	exe(t, onlyFFmpeg, "ffmpeg")
	dirs := []string{filepath.Join(root, "a"), filepath.Join(root, "b")}

	tests := []struct {
		name   string
		path   string
		search ffmpeg.Search
		want   ffmpeg.NotFoundError
	}{
		{
			// A wrong chosen path is reported, even though PATH has ffmpeg.
			name:   "chosen path does not exist",
			path:   onPath,
			search: ffmpeg.Search{FFmpeg: filepath.Join(root, "typo", "ffmpeg"), Source: "--ffmpeg"},
			want:   ffmpeg.NotFoundError{Program: "ffmpeg", Path: filepath.Join(root, "typo", "ffmpeg"), Source: "--ffmpeg"},
		},
		{
			name:   "nowhere",
			path:   filepath.Join(root, "empty"),
			search: ffmpeg.Search{Dirs: dirs},
			want:   ffmpeg.NotFoundError{Program: "ffmpeg", Searched: dirs},
		},
		{
			name:   "ffmpeg without ffprobe",
			path:   onlyFFmpeg,
			search: ffmpeg.Search{Dirs: dirs},
			want:   ffmpeg.NotFoundError{Program: "ffprobe", Searched: dirs},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", tt.path)
			_, err := ffmpeg.Locate(tt.search)
			var nf *ffmpeg.NotFoundError
			if !errors.As(err, &nf) {
				t.Fatalf("err = %v, want a NotFoundError", err)
			}
			if nf.Program != tt.want.Program || nf.Path != tt.want.Path || nf.Source != tt.want.Source || !slices.Equal(nf.Searched, tt.want.Searched) {
				t.Errorf("got  %+v\nwant %+v", *nf, tt.want)
			}
			if (tt.want.Path != "") != (nf.Err != nil) {
				t.Errorf("Err = %v", nf.Err)
			}
			if tt.want.Path != "" && !errors.Is(nf.Err, fs.ErrNotExist) {
				t.Errorf("Err = %v, want it to say the file does not exist", nf.Err)
			}
		})
	}
}

func TestInstallCommands(t *testing.T) {
	osRelease := map[string]string{
		"mint":       "NAME=\"Linux Mint\"\nID=linuxmint\nID_LIKE=\"ubuntu debian\"\n",
		"tumbleweed": "NAME=\"openSUSE Tumbleweed\"\nID=\"opensuse-tumbleweed\"\nID_LIKE=\"opensuse suse\"\n",
		"fedora":     "NAME=\"Fedora Linux\"\nID=fedora\n",
		"rocky":      "NAME=\"Rocky Linux\"\nID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n",
		"manjaro":    "NAME=\"Manjaro Linux\"\r\nID=manjaro\r\nID_LIKE=arch\r\n",
		"unknown":    "ID=plan10\n",
	}
	tests := []struct {
		goos, distro string
		first        string // "" means no commands, only the download link
	}{
		{"linux", "mint", "sudo apt install ffmpeg"},
		{"linux", "tumbleweed", "sudo zypper install ffmpeg"},
		{"linux", "fedora", "sudo dnf install ffmpeg-free"},
		// Fedora's package is not in RHEL's repositories, so Rocky gets the
		// download link instead.
		{"linux", "rocky", ""},
		{"linux", "manjaro", "sudo pacman -S ffmpeg"},
		{"linux", "unknown", ""},
		{"darwin", "", "brew install ffmpeg"},
		{"windows", "", "winget install --id Gyan.FFmpeg"},
		{"freebsd", "", "sudo pkg install ffmpeg"},
		{"openbsd", "", "doas pkg_add ffmpeg"},
		{"plan9", "", ""},
	}
	for _, tt := range tests {
		got := ffmpeg.InstallCommands(tt.goos, ffmpeg.ParseOSRelease(osRelease[tt.distro]))
		first := ""
		if len(got) > 0 {
			first = got[0]
		}
		if first != tt.first {
			t.Errorf("%s %s: got %q, want first %q", tt.goos, tt.distro, got, tt.first)
		}
	}
}
