package ffmpeg

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Search says where to look for ffmpeg and ffprobe.
type Search struct {
	// FFmpeg is a path the user chose, with Source naming where it came from
	// (such as "--ffmpeg" or "FFTUI_FFMPEG"). When set, it is the only ffmpeg
	// tried, so a wrong path is reported instead of silently replaced.
	FFmpeg string
	Source string
	// FFprobe is a path the user chose for ffprobe. When empty, ffprobe is
	// looked for next to ffmpeg, then the same way as ffmpeg.
	FFprobe string
	// Dirs are folders to try after PATH, usually CommonDirs().
	Dirs []string
}

// Binaries are the programs fftui runs.
type Binaries struct {
	FFmpeg  string
	FFprobe string
	// Source says how ffmpeg was found: the Search.Source of a chosen path,
	// "PATH", or the folder it was found in.
	Source string
}

// NotFoundError reports that ffmpeg or ffprobe could not be found.
type NotFoundError struct {
	Program string // "ffmpeg" or "ffprobe"
	// Path and Source are set when the user chose a path that does not work.
	Path   string
	Source string
	Err    error
	// Searched lists the folders tried after PATH.
	Searched []string
}

func (e *NotFoundError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s from %s (%s) does not work: %v", e.Program, e.Source, e.Path, e.Err)
	}
	return e.Program + " was not found on PATH or in the usual install folders"
}

func (e *NotFoundError) Unwrap() error { return e.Err }

// Locate finds ffmpeg and ffprobe. It only looks at the file system; it does
// not run either program.
func Locate(s Search) (Binaries, error) {
	var b Binaries
	if s.FFmpeg != "" {
		path, err := lookExplicit(s.FFmpeg)
		if err != nil {
			return b, &NotFoundError{Program: "ffmpeg", Path: s.FFmpeg, Source: s.Source, Err: err}
		}
		b.FFmpeg, b.Source = path, s.Source
	} else {
		path, where, ok := lookDefault("ffmpeg", s.Dirs)
		if !ok {
			return b, &NotFoundError{Program: "ffmpeg", Searched: s.Dirs}
		}
		b.FFmpeg, b.Source = path, where
	}

	if s.FFprobe != "" {
		path, err := lookExplicit(s.FFprobe)
		if err != nil {
			return b, &NotFoundError{Program: "ffprobe", Path: s.FFprobe, Source: "config ffprobe_path", Err: err}
		}
		b.FFprobe = path
		return b, nil
	}
	// Prefer the ffprobe from the same build as ffmpeg. LookPath adds .exe
	// on Windows.
	if path, err := exec.LookPath(filepath.Join(filepath.Dir(b.FFmpeg), "ffprobe")); err == nil {
		b.FFprobe = path
		return b, nil
	}
	path, _, ok := lookDefault("ffprobe", s.Dirs)
	if !ok {
		return b, &NotFoundError{Program: "ffprobe", Searched: s.Dirs}
	}
	b.FFprobe = path
	return b, nil
}

// lookExplicit checks a path the user typed, expanding a leading ~. A bare
// name such as "ffmpeg7" is looked up on PATH.
func lookExplicit(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[1:])
	}
	found, err := exec.LookPath(path)
	// Keep only the reason, such as "no such file or directory"; the error
	// message already names the path.
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		err = execErr.Err
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return found, err
}

// lookDefault looks for name on PATH, then in dirs. exec.LookPath adds .exe
// on Windows and checks that the file is executable elsewhere.
func lookDefault(name string, dirs []string) (path, where string, ok bool) {
	if path, err := exec.LookPath(name); err == nil {
		return path, "PATH", true
	}
	for _, dir := range dirs {
		if path, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return path, dir, true
		}
	}
	return "", "", false
}

// CommonDirs returns folders where package managers install FFmpeg that are
// often missing from PATH, such as when fftui is started from a GUI launcher.
func CommonDirs() []string {
	var dirs []string
	add := func(base string, elem ...string) {
		if base != "" {
			dirs = append(dirs, filepath.Join(append([]string{base}, elem...)...))
		}
	}
	switch runtime.GOOS {
	case "windows":
		add(os.Getenv("USERPROFILE"), "scoop", "shims")
		add(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Links")
		add(os.Getenv("ProgramData"), "chocolatey", "bin")
		add(os.Getenv("ProgramFiles"), "ffmpeg", "bin")
		if drive := os.Getenv("SystemDrive"); drive != "" {
			add(drive+`\`, "ffmpeg", "bin")
		}
	case "darwin":
		dirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}
	case "linux":
		dirs = []string{"/usr/local/bin", "/usr/bin", "/snap/bin", "/home/linuxbrew/.linuxbrew/bin"}
		add(os.Getenv("HOME"), ".local", "bin")
		add(os.Getenv("PREFIX"), "bin") // Termux
	default: // BSDs, illumos, Solaris
		dirs = []string{"/usr/local/bin", "/usr/pkg/bin", "/opt/local/bin", "/usr/bin"}
	}
	return dirs
}
