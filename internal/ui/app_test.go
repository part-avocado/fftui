package ui_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/part-avocado/fftui/internal/config"
	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/job"
	"github.com/part-avocado/fftui/internal/ui"
)

const configPath = "/home/me/.config/fftui/config.toml"

var (
	tab      = tea.KeyPressMsg{Code: tea.KeyTab}
	shiftTab = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	down     = tea.KeyPressMsg{Code: tea.KeyDown}
	pgDown   = tea.KeyPressMsg{Code: tea.KeyPgDown}
	pgUp     = tea.KeyPressMsg{Code: tea.KeyPgUp}
)

func char(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// drive runs cmd and feeds every message it produces back into m, the way
// tea.Program does, until no commands are left.
func drive(m tea.Model, cmd tea.Cmd) tea.Model {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.QuitMsg:
		default:
			var next tea.Cmd
			m, next = m.Update(msg)
			queue = append(queue, next)
		}
	}
	return m
}

func send(m tea.Model, msgs ...tea.Msg) tea.Model {
	for _, msg := range msgs {
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		m = drive(m, cmd)
	}
	return m
}

func screen(m tea.Model) string {
	return ansi.Strip(m.View().Content)
}

// fixtureDir holds the FFmpeg 9.0 fixtures. It is absolute, so tests that
// change into a temporary folder still find it.
var fixtureDir = func() string {
	dir, err := filepath.Abs(filepath.Join("..", "ffmpeg", "testdata", "ffmpeg-9.0"))
	if err != nil {
		panic(err)
	}
	return dir
}()

// workDir changes into a new temporary folder for the rest of the test and
// returns ".", so the screens show short relative paths on every OS rather
// than a long temporary folder that wraps or is cut off.
func workDir(t *testing.T) string {
	t.Chdir(t.TempDir())
	return "."
}

// fakeTools is the FFmpeg 9.0 build in ../ffmpeg/testdata. Its ffprobe
// answers with the fixtures that real ffprobe wrote for files with these
// names, in any folder, and fails like ffprobe for any other.
func fakeTools(t *testing.T) *ffmpeg.Tools {
	t.Helper()
	dir := fixtureDir
	fixtures := map[string]string{
		"multi stream.mkv": "probe-multi.json",
		"song.mp3":         "probe-song.json",
		"sample.mp4":       "probe-sample.json",
	}
	r := ffmpeg.RunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/usr/bin/ffmpeg" {
			return os.ReadFile(filepath.Join(dir, strings.TrimPrefix(args[1], "-")+".txt"))
		}
		path := args[len(args)-1]
		if name, ok := fixtures[filepath.Base(path)]; ok {
			return os.ReadFile(filepath.Join(dir, name))
		}
		return nil, &ffmpeg.RunError{Program: "ffprobe", Err: errors.New("exit status 1"), Stderr: path + ": Invalid data found when processing input"}
	})
	caps, err := ffmpeg.Detect(context.Background(), r, "/usr/bin/ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	return &ffmpeg.Tools{
		Binaries: ffmpeg.Binaries{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe", Source: "PATH"},
		Caps:     caps,
		Runner:   r,
	}
}

type appOpts struct {
	cfg     config.Config
	files   []string
	find    func(context.Context, config.Config) (*ffmpeg.Tools, error)
	size    tea.WindowSizeMsg
	queue   *job.Queue
	command []string
	now     func() time.Time
	// configPath defaults to the constant configPath; copy records copies.
	configPath string
	ascii      bool
	copy       func(string) error
}

func newApp(t *testing.T, o appOpts) tea.Model {
	t.Helper()
	if o.cfg == (config.Config{}) {
		o.cfg = config.Default()
	}
	if o.find == nil {
		tools := fakeTools(t)
		o.find = func(context.Context, config.Config) (*ffmpeg.Tools, error) { return tools, nil }
	}
	if o.configPath == "" {
		o.configPath = configPath
	}
	if o.copy == nil {
		o.copy = func(string) error { return nil }
	}
	if o.size.Width == 0 {
		o.size = tea.WindowSizeMsg{Width: 80, Height: 24}
	}
	if o.queue == nil {
		o.queue = job.NewQueue(1, newFakeJobs().start)
	}
	app := ui.New(ui.Options{
		Config:     o.cfg,
		ConfigPath: o.configPath,
		ASCII:      o.ascii,
		Copy:       o.copy,
		FindFFmpeg: o.find,
		Install:    []string{"brew install ffmpeg", "sudo port install ffmpeg"},
		Files:      o.files,
		Jobs:       o.queue,
		Command:    o.command,
		Now:        o.now,
	})
	return send(drive(app, app.Init()), o.size)
}

func notFound(context.Context, config.Config) (*ffmpeg.Tools, error) {
	return nil, &ffmpeg.NotFoundError{Program: "ffmpeg", Searched: []string{"/opt/homebrew/bin", "/usr/local/bin"}}
}

func assertContains(t *testing.T, m tea.Model, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(screen(m), want) {
			t.Errorf("screen does not contain %q:\n%s", want, screen(m))
		}
	}
}

func TestScreenSwitching(t *testing.T) {
	const (
		files    = "No files yet"
		jobs     = "No jobs yet"
		settings = "Config file: " + configPath
	)
	steps := []struct {
		name string
		key  tea.Msg
		want string
	}{
		{"tab goes to the next screen", tab, jobs},
		{"tab again", tab, settings},
		{"tab wraps to the first screen", tab, files},
		{"shift+tab wraps to the last screen", shiftTab, settings},
		{"shift+tab goes back", shiftTab, jobs},
		{"digit jumps to that screen", char('3'), settings},
		{"digit 1 jumps to the first screen", char('1'), files},
	}

	m := newApp(t, appOpts{})
	assertContains(t, m, files)
	for _, s := range steps {
		m = send(m, s.key)
		if !strings.Contains(screen(m), s.want) {
			t.Fatalf("%s: screen does not contain %q:\n%s", s.name, s.want, screen(m))
		}
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{char('q'), {Code: 'c', Mod: tea.ModCtrl}} {
		_, cmd := newApp(t, appOpts{}).Update(k)
		if cmd == nil {
			t.Fatalf("%s: no command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s: command did not quit", k)
		}
	}
}

func TestSettingsShowsConfigAndFFmpeg(t *testing.T) {
	m := send(newApp(t, appOpts{cfg: config.Config{Jobs: 3, OutputDir: "/srv/out"}}), char('3'))
	assertContains(t, m,
		"jobs          3",
		"output_dir    /srv/out",
		"ffmpeg_path   (find automatically)",
		"ffmpeg        /usr/bin/ffmpeg (from PATH)",
		"version       n9.0.2-48-gd4070a14ce-20261110",
		"hw encoders   29 (NVIDIA NVENC, Intel Quick Sync, AMD AMF, VA-API, Vulkan,",
	)
	if strings.Contains(screen(m), "older than 4.4") {
		t.Error("9.0 is reported as too old")
	}

	old := fakeTools(t)
	old.Caps.Version = ffmpeg.Version{String: "4.3.2", Major: 4, Minor: 3, Known: true}
	m = send(newApp(t, appOpts{find: func(context.Context, config.Config) (*ffmpeg.Tools, error) { return old, nil }}), char('3'))
	assertContains(t, m, "older than 4.4")
}

func TestMediaInfo(t *testing.T) {
	m := newApp(t, appOpts{files: []string{"multi stream.mkv", "song.mp3", "notes.txt"}})
	// The list summarizes every file once ffprobe has answered.
	assertContains(t, m,
		"> multi stream.mkv  0:00:03.026  1 video, 2 audio, 1 subtitle",
		"  song.mp3  0:00:02.",
		"  notes.txt  can't read",
	)
	// Values from the ffprobe fixture: size 2984611 bytes is 2.85 MiB, and
	// bit_rate 7890577 is 7.9 Mb/s.
	assertContains(t, m,
		"Container Matroska / WebM (matroska,webm)",
		"Duration  0:00:03.026",
		"Size      2.8 MiB",
		"Bit rate  7.9 Mb/s",
		"Title     Big Buck Test",
		"Streams (4)",
		" 0  video     h264 (High 10)  1280x720  29.97 fps  yuv420p10le",
		" 1  audio     opus  48 kHz  5.1  eng",
		` 2  audio     aac (LC)  44.1 kHz  stereo  jpn  "Commentary"`,
		" 3  subtitle  subrip  eng  forced",
	)

	m = send(m, down)
	assertContains(t, m,
		"> song.mp3",
		"Title     Sine Song",
		" 0  audio     mp3  44.1 kHz  mono  192 kb/s",
		" 1  video     mjpeg (Baseline)  300x300  yuvj420p  cover art",
	)
	if strings.Contains(screen(m), "fps") {
		t.Errorf("cover art has a frame rate:\n%s", screen(m))
	}

	m = send(m, down)
	assertContains(t, m, "> notes.txt", "Could not read this file", "ffprobe: notes.txt: Invalid data found when processing input")
	// Down at the last file stays there.
	m = send(m, down)
	assertContains(t, m, "> notes.txt")
}

func TestMediaInfoScrolls(t *testing.T) {
	m := newApp(t, appOpts{files: []string{"multi stream.mkv"}, size: tea.WindowSizeMsg{Width: 80, Height: 14}})
	assertContains(t, m, "Container", "pgup/pgdn scroll")
	if strings.Contains(screen(m), " 3  subtitle") {
		t.Fatalf("all of the info fits; the test needs a smaller terminal:\n%s", screen(m))
	}

	m = send(m, pgDown)
	assertContains(t, m, " 3  subtitle", "> multi stream.mkv")
	if strings.Contains(screen(m), "Container") {
		t.Errorf("page down did not scroll:\n%s", screen(m))
	}
	// Scrolling stops at the last line.
	m = send(m, pgDown, pgDown)
	assertContains(t, m, " 3  subtitle")

	m = send(m, pgUp)
	assertContains(t, m, "Container")
}

func TestMissingFFmpeg(t *testing.T) {
	calls := 0
	tools := fakeTools(t)
	find := func(ctx context.Context, _ config.Config) (*ffmpeg.Tools, error) {
		calls++
		if calls == 1 {
			return notFound(ctx, config.Config{})
		}
		return tools, nil
	}
	m := newApp(t, appOpts{files: []string{"song.mp3"}, find: find})
	assertContains(t, m,
		"ffmpeg not found",
		"Looked on PATH and in /opt/homebrew/bin, /usr/local/bin",
		"brew install ffmpeg",
		"sudo port install ffmpeg",
		"https://ffmpeg.org/download.html",
		"fftui --ffmpeg /path/to/ffmpeg",
		configPath,
		"r look again",
	)
	assertContains(t, send(m, char('3')), "Not usable: ffmpeg was not found")

	// Installing FFmpeg and pressing r finds it and reads the files.
	m = send(m, char('r'))
	if calls != 2 {
		t.Fatalf("FindFFmpeg ran %d times, want 2", calls)
	}
	assertContains(t, m, "> song.mp3  0:00:02.", "Title     Sine Song")
	if strings.Contains(screen(m), "look again") {
		t.Errorf("retry key still shown:\n%s", screen(m))
	}
}

func TestChosenFFmpegDoesNotWork(t *testing.T) {
	find := func(context.Context, config.Config) (*ffmpeg.Tools, error) {
		return nil, &ffmpeg.NotFoundError{Program: "ffmpeg", Path: "/opt/typo/ffmpeg", Source: "--ffmpeg", Err: errors.New("no such file")}
	}
	m := newApp(t, appOpts{find: find})
	assertContains(t, m, "The chosen ffmpeg does not work", "ffmpeg from --ffmpeg (/opt/typo/ffmpeg) does not work: no such file")
	// Install commands would not help when the user chose a wrong path.
	if strings.Contains(screen(m), "brew install") {
		t.Errorf("install commands shown for a wrong path:\n%s", screen(m))
	}
}

// states returns a way to build the app in each state a screen can be in.
// Every call builds a new app: copies of an App share its screens, so a
// state built from another one would change it.
func states(t *testing.T, cfg config.Config, size tea.WindowSizeMsg) map[string]func() tea.Model {
	files := []string{"multi stream.mkv", "song.mp3", "notes.txt"}
	jobs := func(keys ...tea.Msg) func() tea.Model {
		return func() tea.Model {
			f := newFakeJobs()
			q := job.NewQueue(1, f.start)
			m := newApp(t, appOpts{cfg: cfg, size: size, queue: q,
				command: []string{"-i", "multi stream.mkv", "-c:v", "mpeg4", "-metadata", "title=A rather long title for wrapping", "out.mkv"}})
			q.Add(job.Spec{Name: "queued with a very long name that will not fit on one line of the list.mkv"})
			p := f.proc(t, "out.mkv")
			p.ev.Progress(ffmpeg.Progress{OutTime: time.Second, Speed: 1.5, TotalSize: 5 << 20})
			for i := range 100 {
				p.ev.Log(fmt.Sprintf("[mpeg4 @ 0x5600] a long FFmpeg warning that wraps across the screen, number %d of many", i))
			}
			return send(m, append([]tea.Msg{ui.JobsChanged{}}, keys...)...)
		}
	}
	// A video whose trimmed output already exists.
	dir := t.TempDir()
	sample := filepath.Join(dir, "sample.mp4")
	for _, name := range []string{sample, filepath.Join(dir, "sample-trim.mp4")} {
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	video := func(keys ...tea.Msg) func() tea.Model {
		return func() tea.Model {
			return send(newApp(t, appOpts{cfg: cfg, size: size, files: []string{sample}}), keys...)
		}
	}
	trim := []tea.Msg{enter, down, down, down, down, enter}
	app := func(o appOpts, keys ...tea.Msg) func() tea.Model {
		return func() tea.Model { o.cfg, o.size = cfg, size; return send(newApp(t, o), keys...) }
	}
	// Two videos with the same name in different folders, and a song.
	batch := []string{filepath.Join(dir, "a", "sample.mp4"), filepath.Join(dir, "b", "sample.mp4"), filepath.Join(dir, "song.mp3")}
	for _, name := range batch {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]func() tea.Model{
		"no files":         app(appOpts{}),
		"media info":       app(appOpts{files: files}),
		"missing ffmpeg":   app(appOpts{files: files, find: notFound}),
		"running job":      jobs(),
		"log view":         jobs(char('l')),
		"cancel prompt":    jobs(char('x')),
		"quit prompt":      jobs(char('q')),
		"preset menu":      video(enter),
		"two-pass form":    video(enter, down, down, enter),
		"form error":       video(append(trim, char('x'))...),
		"overwrite prompt": video(append(trim, enter)...),
		"marked files":     app(appOpts{files: batch}, space),
		"batch menu":       app(appOpts{files: batch}, char('a'), enter),
		"batch form":       app(appOpts{files: batch}, space, space, enter, enter),
		"custom command":   video(enter, up, enter),
		"copied notice":    video(enter, enter, tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}),
		"settings editor":  app(appOpts{}, char('3'), char('e'), ctrlU, char('x')),
	}
}

func TestViewFitsTerminal(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 120, Height: 40}, {Width: 60, Height: 16}} {
		for name, build := range states(t, config.Default(), size) {
			for _, keys := range [][]tea.Msg{nil, {char('?')}, {char('2')}, {char('3')}, {char('3'), char('?')}} {
				m := send(build(), keys...)
				lines := strings.Split(screen(m), "\n")
				if len(lines) != size.Height {
					t.Errorf("%dx%d %s %v: %d lines, want %d:\n%s", size.Width, size.Height, name, keys, len(lines), size.Height, screen(m))
				}
				for i, line := range lines {
					if w := ansi.StringWidth(line); w > size.Width {
						t.Errorf("%dx%d %s %v: line %d is %d cells wide: %q", size.Width, size.Height, name, keys, i+1, w, line)
					}
				}
				// A screen that draws too much pushes the border's bottom off.
				if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "╰") }) {
					t.Errorf("%dx%d %s %v: the body overflows its border:\n%s", size.Width, size.Height, name, keys, screen(m))
				}
				// A notice stands in for the help bar until the next key.
				if !strings.Contains(lines[len(lines)-1]+lines[len(lines)-2], "quit") && (name != "copied notice" || keys != nil) {
					t.Errorf("%dx%d %s %v: the help bar lost the quit key:\n%s", size.Width, size.Height, name, keys, screen(m))
				}
			}
		}
	}
}

func TestASCIIModeDrawsOnlyASCII(t *testing.T) {
	for name, build := range states(t, config.Config{Jobs: 1, ASCII: true}, tea.WindowSizeMsg{Width: 80, Height: 24}) {
		m := build()
		for _, msg := range []tea.Msg{nil, char('?'), tab, tab} {
			if msg != nil {
				m = send(m, msg)
			}
			for i, r := range screen(m) {
				if r > 0x7e {
					t.Fatalf("%s: non-ASCII %q at byte %d:\n%s", name, r, i, screen(m))
				}
			}
		}
	}
}
