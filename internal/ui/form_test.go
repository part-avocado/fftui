package ui_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/job"
	"github.com/part-avocado/fftui/internal/ui"
)

var (
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	up    = tea.KeyPressMsg{Code: tea.KeyUp}
	left  = tea.KeyPressMsg{Code: tea.KeyLeft}
	right = tea.KeyPressMsg{Code: tea.KeyRight}
	ctrlU = tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
)

// presetApp opens the app on sample.mp4 in a new current folder; the fake
// ffprobe describes it as 2s of 320x240 H.264 with AAC audio.
func presetApp(t *testing.T) (m tea.Model, f *fakeJobs, q *job.Queue, dir string) {
	t.Helper()
	dir = workDir(t)
	in := filepath.Join(dir, "sample.mp4")
	if err := os.WriteFile(in, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f = newFakeJobs()
	q = job.NewQueue(1, f.start)
	return newApp(t, appOpts{files: []string{in}, queue: q}), f, q, dir
}

// openPreset opens the menu and the form of the preset with this name.
func openPreset(t *testing.T, m tea.Model, name string) tea.Model {
	t.Helper()
	m = send(m, enter)
	for range 10 {
		if strings.Contains(screen(m), "> "+name) {
			return send(m, enter)
		}
		m = send(m, down)
	}
	t.Fatalf("no preset %q:\n%s", name, screen(m))
	return nil
}

// focus moves down the form to the field with this label.
func focus(t *testing.T, m tea.Model, label string) tea.Model {
	t.Helper()
	for range 12 {
		if strings.Contains(screen(m), "> "+label) {
			return m
		}
		m = send(m, down)
	}
	t.Fatalf("no field %q:\n%s", label, screen(m))
	return nil
}

func typeText(m tea.Model, s string) tea.Model {
	for _, r := range s {
		m = send(m, char(r))
	}
	return m
}

func TestPresetMenu(t *testing.T) {
	m, _, _, _ := presetApp(t)
	m = send(m, enter)
	assertContains(t, m, "Choose a task for sample.mp4", "> Convert video", "Make a GIF",
		// A preset that cannot work says why.
		"Resize              can't: the video is only 240 pixels tall")

	// Enter on a preset that cannot work does nothing.
	// Up from the first task wraps to the custom command at the end.
	m = send(m, up)
	assertContains(t, m, "> Custom command")
	m = send(m, up, up, up)
	assertContains(t, m, "> Resize")
	m = send(m, enter)
	assertContains(t, m, "Choose a task for sample.mp4")

	m = send(m, down, enter)
	assertContains(t, m, "Make a GIF  •  sample.mp4", "> Start")
	// Esc goes back to the menu, and again to the file.
	m = send(m, esc)
	assertContains(t, m, "> Make a GIF")
	m = send(m, esc)
	assertContains(t, m, "sample.mp4  0:00:02.000", "Container")
}

func TestFilesUpDown(t *testing.T) {
	m := newApp(t, appOpts{files: []string{"multi stream.mkv", "song.mp3"}})
	m = send(m, down)
	assertContains(t, m, "> song.mp3")
	m = send(m, up)
	assertContains(t, m, "> multi stream.mkv")
	// Up at the first file stays there.
	m = send(m, up)
	assertContains(t, m, "> multi stream.mkv")
}

func TestFormCommandFollowsFields(t *testing.T) {
	m, f, _, dir := presetApp(t)
	in, out := filepath.Join(dir, "sample.mp4"), filepath.Join(dir, "sample-converted.mp4")
	m = openPreset(t, m, "Convert video")
	assertContains(t, m, "> Container  ‹ MP4 ›", "Encoder    libx264 (software)", "-c:v libx264 -crf 23", "+faststart")

	// MKV changes the command and the default output.
	m = send(m, right)
	assertContains(t, m, "> Container  ‹ MKV ›", "sample-converted.mkv")
	assertMissing(t, m, "faststart")
	m = send(m, left)
	assertContains(t, m, "‹ MP4 ›", "+faststart")

	m = focus(t, m, "Quality")
	m = send(m, left)
	assertContains(t, m, "-crf 18")

	m = send(m, enter)
	assertContains(t, m, "> running   sample-converted.mp4")
	if len(f.specs) != 1 {
		t.Fatalf("started %d jobs, want 1", len(f.specs))
	}
	s := f.specs[0]
	want := []string{"-i", in, "-map", "0:0", "-map", "0:1", "-c:v", "libx264", "-crf", "18", "-preset", "medium",
		"-pix_fmt", "yuv420p", "-c:a", "copy", "-movflags", "+faststart", out}
	if len(s.Steps) != 1 || !slices.Equal(s.Steps[0], want) {
		t.Errorf("steps = %q\nwant %q", s.Steps, want)
	}
	if s.Output != out || s.Program != "/usr/bin/ffmpeg" || s.Duration != 2*time.Second || s.Retry != nil {
		t.Errorf("spec = %+v", s)
	}
}

func TestFormTextFieldTakesEveryKey(t *testing.T) {
	m, _, _, _ := presetApp(t)
	m = openPreset(t, m, "Trim")
	assertContains(t, m, "> Start   0:00:00.000")
	// These keys switch screens or quit elsewhere; here they are text.
	m = send(m, ctrlU)
	for _, k := range []tea.KeyPressMsg{char('q'), char('2'), char('?')} {
		var cmd tea.Cmd
		m, cmd = m.Update(k)
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); ok {
				t.Fatalf("%s quit", k)
			}
		}
		m = drive(m, cmd)
	}
	assertContains(t, m, "> Start   q2?", "Can't start: start:")
	assertMissing(t, m, "No jobs yet", "Command")

	// Tab moves to the next field instead of the next screen.
	m = send(m, tab)
	assertContains(t, m, "> End", "Trim  •  sample.mp4")

	// ctrl+c still quits.
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd == nil {
		t.Fatal("ctrl+c did nothing")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+c did not quit")
	}
}

func TestFormEditsText(t *testing.T) {
	m, _, _, _ := presetApp(t)
	m = openPreset(t, m, "Trim")
	m = send(m, ctrlU)
	m = typeText(m, "0:01")
	// The cursor moves, and typing goes in at the cursor.
	m = send(m, left, left, char('0'))
	assertContains(t, m, "> Start   0:001")
	m = send(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	assertContains(t, m, "> Start   0:01")
	m = send(m, tea.KeyPressMsg{Code: tea.KeyHome}, tea.KeyPressMsg{Code: tea.KeyDelete})
	assertContains(t, m, "> Start   :01")
	m = send(m, tea.PasteMsg{Content: "1.5\n"})
	assertContains(t, m, "> Start   1.5:01", "Can't start: start:")
	m = send(m, ctrlU, tea.PasteMsg{Content: "1.5"})
	assertContains(t, m, "> Start   1.5", "-ss 1.5", "-t 0.5")

	m = send(m, down)
	m = send(m, ctrlU, char('1'))
	assertContains(t, m, "Can't start: the end must be after the start")
	// Enter does not start a form with an error.
	m = send(m, enter)
	assertContains(t, m, "Trim  •  sample.mp4")
}

func TestFormOutput(t *testing.T) {
	m, f, _, dir := presetApp(t)
	in := filepath.Join(dir, "sample.mp4")
	m = openPreset(t, m, "Trim")
	m = focus(t, m, "Output")
	assertContains(t, m, "sample-trim.mp4")

	m = send(m, ctrlU, tea.PasteMsg{Content: in})
	assertContains(t, m, "Can't start: the output is the input file; choose another name")
	m = send(m, ctrlU, tea.PasteMsg{Content: dir})
	assertContains(t, m, "Can't start: the output is a folder; add a file name")
	m = send(m, ctrlU, tea.PasteMsg{Content: filepath.Join(dir, "nope", "a.mp4")})
	assertContains(t, m, "Can't start: the folder", filepath.Join(dir, "nope"))

	// An edited output no longer follows the other fields.
	out := filepath.Join(dir, "clip.mp4")
	m = send(m, ctrlU, tea.PasteMsg{Content: out})
	m = send(m, up, right)
	assertContains(t, m, "Exact (re-encodes)", "clip.mp4")

	// An existing output asks before overwriting it.
	if err := os.WriteFile(out, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m = send(m, left)
	assertContains(t, m, "clip.mp4 already exists; enter asks before overwriting it.")
	m = send(m, enter)
	assertContains(t, m, "clip.mp4 already exists. Overwrite it?", "y overwrite", "n keep it")
	m = send(m, char('n'))
	assertContains(t, m, "Trim  •  sample.mp4")
	if len(f.specs) != 0 {
		t.Fatal("n started the job")
	}
	m = send(m, enter, char('y'))
	assertContains(t, m, "> running   clip.mp4")
	if len(f.specs) != 1 || f.specs[0].Steps[0][0] != "-y" || f.specs[0].Output != out {
		t.Fatalf("started %+v, want one job that overwrites %s", f.specs, out)
	}
}

func TestHardwareEncoderRetriesInSoftware(t *testing.T) {
	m, f, q, _ := presetApp(t)
	m = openPreset(t, m, "Convert video")
	m = focus(t, m, "Encoder")
	m = send(m, right)
	assertContains(t, m, "‹ h264_amf (AMD AMF) ›", "-c:v h264_amf")
	m = send(m, enter)
	s := f.specs[0]
	if s.Retry == nil || encoder(s.Retry.Steps[0]) != "libx264" || s.Retry.Output != s.Output {
		t.Fatalf("retry = %+v, want the same job with libx264", s.Retry)
	}

	p := f.proc(t, "sample-converted.mp4")
	finish(t, q, p, &ffmpeg.RunError{Program: "ffmpeg", Err: errors.New("exit status 1"), Stderr: "DLL amfrt64.dll failed to open"})
	m = send(m, ui.JobsChanged{})
	assertContains(t, m, "> failed", "Press s to run it again", "with libx264.", "s retry in software")

	m = send(m, char('s'))
	if len(f.specs) != 2 || encoder(f.specs[1].Steps[0]) != "libx264" {
		t.Fatalf("started %+v, want the software retry", f.specs)
	}
	assertContains(t, m, "> running   sample-converted.mp4", "  failed    sample-converted.mp4")
	assertMissing(t, m, "s retry in software")
}

func encoder(args []string) string {
	if i := slices.Index(args, "-c:v"); i >= 0 {
		return args[i+1]
	}
	return ""
}

func TestTwoPassJob(t *testing.T) {
	m, f, q, _ := presetApp(t)
	m = openPreset(t, m, "Compress to a size")
	m = send(m, ctrlU)
	m = typeText(m, "1")
	// On a small terminal every field shows, and the command is cut.
	assertContains(t, m, "Passes       Two (closer to the size)", "Audio        128 kb/s", "Output       ",
		"Pass 1: ffmpeg", "(a taller terminal shows the rest)")
	m = send(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	assertContains(t, m, "Pass 1: ffmpeg", "Pass 2: ffmpeg")
	assertMissing(t, m, "taller terminal")
	m = send(m, enter)
	s := f.specs[0]
	if len(s.Steps) != 2 || len(s.Temp) == 0 {
		t.Fatalf("spec = %+v, want two steps and a pass log to delete", s)
	}

	p := f.proc(t, "sample-small.mp4")
	p.ev.Progress(ffmpeg.Progress{OutTime: time.Second})
	m = send(m, ui.JobsChanged{})
	// Half of the first of two passes is a quarter of the job.
	assertContains(t, m, "> running   sample-small.mp4  pass 1 of 2 25%", "pass 1 of 2")
	// On a small terminal the long commands give way to the progress.
	small := send(m, tea.WindowSizeMsg{Width: 60, Height: 16})
	assertContains(t, small, "█", "pass 1 of 2 • 0:00:01 of 0:00:02")

	p.ev.Progress(ffmpeg.Progress{OutTime: 2 * time.Second, End: true})
	p.exit <- nil
	for deadline := time.Now().Add(5 * time.Second); q.Jobs()[0].Step == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the second pass did not start")
		}
	}
	m = send(m, ui.JobsChanged{})
	assertContains(t, m, "> running   sample-small.mp4  pass 2 of 2 50%", "pass 2 of 2")
}
