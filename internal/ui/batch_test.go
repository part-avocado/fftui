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

	tea "charm.land/bubbletea/v2"

	"github.com/part-avocado/fftui/internal/clipboard"
	"github.com/part-avocado/fftui/internal/config"
	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/job"
)

var space = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}

// touch creates empty files for the fake ffprobe to describe.
func touch(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBatch(t *testing.T) {
	dir := t.TempDir()
	a, b, song := filepath.Join(dir, "a", "sample.mp4"), filepath.Join(dir, "b", "sample.mp4"), filepath.Join(dir, "song.mp3")
	touch(t, a, b, song)
	f := newFakeJobs()
	q := job.NewQueue(1, f.start)
	m := newApp(t, appOpts{files: []string{a, b, song}, queue: q})

	// space marks a file and moves to the next.
	m = send(m, space)
	// Unmarked files are indented to line up with the marked ones.
	assertContains(t, m, "✓ "+a, ">   "+b, "enter task for 1 marked")
	m = send(m, space)
	assertContains(t, m, "✓ "+b, ">   "+song, "enter task for 2 marked")

	m = send(m, enter)
	assertContains(t, m, "Choose a task for 2 files", "> Convert video",
		"Custom command      can't: works on one file at a time")
	m = send(m, enter)
	assertContains(t, m, "Convert video  •  2 files", "Output folder", "empty: next to each input",
		"Command for sample.mp4 (and 1 more like it)")

	// Each input's output goes next to it.
	m = send(m, enter)
	if len(f.specs) != 1 || len(q.Jobs()) != 2 {
		t.Fatalf("started %d and queued %d jobs, want 1 running of 2 (jobs = 1)", len(f.specs), len(q.Jobs()))
	}
	for i, in := range []string{a, b} {
		s := q.Jobs()[i].Spec
		if want := filepath.Join(filepath.Dir(in), "sample-converted.mp4"); s.Output != want || s.Steps[0][1] != in {
			t.Errorf("job %d: output %s from %s, want %s from %s", i, s.Output, s.Steps[0][1], want, in)
		}
	}
	assertContains(t, m, "> running   sample-converted.mp4", "  queued    sample-converted.mp4")

	// The marks are gone once the batch starts.
	m = send(m, char('1'))
	assertMissing(t, m, "✓")
}

func TestBatchShowsItsQueuedJobs(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a", "sample.mp4"), filepath.Join(dir, "b", "sample.mp4")
	touch(t, a, b)
	q := job.NewQueue(1, newFakeJobs().start)
	m := newApp(t, appOpts{files: []string{a, b}, queue: q})
	// Earlier jobs fill more than the list's height.
	for i := range 6 {
		q.Add(job.Spec{Name: fmt.Sprintf("earlier%d.mkv", i)})
	}
	m = send(m, char('a'), enter, enter, enter)
	// The batch's first job is selected, and the second shows below it.
	assertContains(t, m, "> queued    sample-converted.mp4", "\n│   queued    sample-converted.mp4")
}

func TestBatchProblems(t *testing.T) {
	dir := t.TempDir()
	a, b, song := filepath.Join(dir, "a", "sample.mp4"), filepath.Join(dir, "b", "sample.mp4"), filepath.Join(dir, "song.mp3")
	touch(t, a, b, song)
	m := newApp(t, appOpts{files: []string{a, b, song}})

	// a marks every file; a again unmarks them.
	m = send(m, char('a'))
	assertContains(t, m, "enter task for 3 marked")
	m = send(m, char('a'))
	assertContains(t, m, "enter choose a task")
	assertMissing(t, m, "✓")
	m = send(m, char('a'))

	// A task that cannot work on one of the files says which.
	m = send(m, enter)
	assertContains(t, m, "Choose a task for 3 files", "Convert video       can't: song.mp3: ")
	m = send(m, esc, char('a'))
	assertMissing(t, m, "✓")

	// Two inputs with the same name cannot write to one output folder.
	m = send(m, space, space, enter, enter)
	m = focus(t, m, "Output folder")
	m = send(m, tea.PasteMsg{Content: dir})
	assertContains(t, m, "Can't start: sample.mp4 and sample.mp4 would both write")
}

func TestCopyCommand(t *testing.T) {
	var copied []string
	copyErr := error(nil)
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "sample.mp4"))
	f := newFakeJobs()
	m := newApp(t, appOpts{files: []string{filepath.Join(dir, "sample.mp4")}, queue: job.NewQueue(1, f.start),
		copy: func(s string) error { copied = append(copied, s); return copyErr }})

	m = openPreset(t, m, "Compress to a size")
	m = send(m, tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if len(copied) != 1 {
		t.Fatalf("copied %q", copied)
	}
	// Both passes, joined so the second runs only if the first worked.
	if c := copied[0]; !strings.HasPrefix(c, "ffmpeg -i "+filepath.Join(dir, "sample.mp4")) || strings.Count(c, " && ffmpeg ") != 1 || !strings.Contains(c, "-pass 2") {
		t.Errorf("copied %q", c)
	}
	assertContains(t, m, "Copied the command.")
	// The next key brings the help back.
	m = send(m, up)
	assertMissing(t, m, "Copied")
	assertContains(t, m, "ctrl+y copy command")

	// Without a clipboard tool, the copy went only through the terminal.
	copyErr = clipboard.ErrNoTool
	m = send(m, tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	assertContains(t, m, "Sent the command to the terminal's clipboard.")
	copyErr = errors.New("xclip: can't open display")
	m = send(m, tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	assertContains(t, m, "the system clipboard failed:", "xclip: can't open display")

	// The Jobs screen copies a job's command.
	copyErr = nil
	m = send(m, enter, char('c'))
	assertContains(t, m, "> running   sample-small.mp4", "Copied the command.")
	if last := copied[len(copied)-1]; last != copied[0] {
		t.Errorf("job copied %q, want the form's %q", last, copied[0])
	}
}

func TestSettingsEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fftui", "config.toml")
	var finds []config.Config
	tools := fakeTools(t)
	find := func(_ context.Context, cfg config.Config) (*ffmpeg.Tools, error) {
		finds = append(finds, cfg)
		return tools, nil
	}
	f := newFakeJobs()
	q := job.NewQueue(1, f.start)
	m := newApp(t, appOpts{configPath: path, queue: q, find: find})
	for _, name := range []string{"a", "b", "c"} {
		q.Add(job.Spec{Name: name})
	}

	m = send(m, char('3'), char('e'))
	assertContains(t, m, "> jobs          1", "enter save", "esc cancel")
	// Keys that switch screens are text while editing.
	m = send(m, ctrlU, char('0'))
	assertContains(t, m, "Can't save: jobs must be a whole number, 1 or more")
	m = send(m, enter)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("invalid settings were saved")
	}

	m = send(m, ctrlU, char('2'), down)
	m = send(m, tea.PasteMsg{Content: filepath.Join(t.TempDir(), "missing")})
	assertContains(t, m, "Can't save: output_dir: the folder")
	m = send(m, ctrlU, down, down, down, right)
	assertContains(t, m, "> ascii         ‹ true (plain ASCII drawing) ›")
	m = send(m, enter)

	// Saved, and applied at once: two jobs run, the theme is ASCII.
	got, err := config.Load(path)
	if err != nil || got != (config.Config{Jobs: 2, ASCII: true}) {
		t.Fatalf("saved %+v, %v", got, err)
	}
	if q.Limit() != 2 || len(f.specs) != 2 {
		t.Errorf("limit %d with %d started, want 2 and 2", q.Limit(), len(f.specs))
	}
	assertContains(t, m, "Saved the settings.")
	m = send(m, down)
	assertContains(t, m, "jobs          2", "ascii         true")
	for i, r := range screen(m) {
		if r > 0x7e {
			t.Fatalf("non-ASCII %q at %d after turning ascii on:\n%s", r, i, screen(m))
		}
	}
	if len(finds) != 1 {
		t.Errorf("looked for FFmpeg %d times, want once: its paths did not change", len(finds))
	}

	// A new ffmpeg_path looks for FFmpeg again with it.
	m = send(m, char('e'), down, down, tea.PasteMsg{Content: "/opt/ff/ffmpeg"}, enter)
	if len(finds) != 2 || finds[1].FFmpegPath != "/opt/ff/ffmpeg" {
		t.Errorf("searches = %+v, want a second with the new path", finds)
	}

	// esc keeps the old settings.
	m = send(m, char('e'), ctrlU, char('7'), esc)
	assertContains(t, m, "jobs          2")
	if got, _ := config.Load(path); got.Jobs != 2 {
		t.Errorf("esc saved jobs = %d", got.Jobs)
	}
}

func TestRawCommand(t *testing.T) {
	m, f, _, dir := presetApp(t)
	in := filepath.Join(dir, "sample.mp4")
	m = openPreset(t, m, "Convert video")
	m = send(m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	assertContains(t, m, "Custom command  •  sample.mp4", "ffmpeg -i "+in, "-crf 23 -preset medium")

	// Raw mode runs the command as written.
	m = send(m, ctrlU, tea.PasteMsg{Content: "ffmpeg -i '" + in + "' \\\n  -vf 'scale=160:-2' \"" + filepath.Join(dir, "small one.mkv") + "\"\n"})
	assertContains(t, m, "FFmpeg will not overwrite a file unless the command has -y.")
	m = send(m, enter)
	want := []string{"-i", in, "-vf", "scale=160:-2", filepath.Join(dir, "small one.mkv")}
	if len(f.specs) != 1 || !slices.Equal(f.specs[0].Steps[0], want) {
		t.Fatalf("started %+v, want %q", f.specs, want)
	}
	// The input's length gives the percentage.
	if s := f.specs[0]; s.Name != "small one.mkv" || s.Duration.Seconds() != 2 {
		t.Errorf("spec = %+v", s)
	}
	assertContains(t, m, "> running   small one.mkv")
}

func TestCustomCommandFromMenu(t *testing.T) {
	m, _, _, dir := presetApp(t)
	m = openPreset(t, m, "Custom command")
	assertContains(t, m, "Custom command  •  sample.mp4", "ffmpeg -i "+filepath.Join(dir, "sample.mp4"), "sample-custom.mp4")
	m = send(m, char(' '), char('"'))
	assertContains(t, m, "Can't run: a quote is not closed")
	m = send(m, enter)
	assertContains(t, m, "Custom command  •  sample.mp4")
	// esc goes back to the menu.
	m = send(m, esc)
	assertContains(t, m, "> Custom command")
}
