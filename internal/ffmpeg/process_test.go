package ffmpeg_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

// recorder collects what a Process reports.
type recorder struct {
	mu       sync.Mutex
	progress []ffmpeg.Progress
	log      []string
}

func (r *recorder) events() ffmpeg.Events {
	return ffmpeg.Events{
		Progress: func(p ffmpeg.Progress) { r.mu.Lock(); r.progress = append(r.progress, p); r.mu.Unlock() },
		Log:      func(l string) { r.mu.Lock(); r.log = append(r.log, l); r.mu.Unlock() },
	}
}

// replay runs a fake FFmpeg that writes stdout and stderr, and returns what
// it reported once it exits.
func replay(t *testing.T, stdout, stderr string) *recorder {
	t.Helper()
	dir := t.TempDir()
	for env, content := range map[string]string{"FFTUI_STDOUT": stdout, "FFTUI_STDERR": stderr} {
		path := filepath.Join(dir, env)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv(env, path)
	}
	prog, args := helper(t, "replay")
	var r recorder
	p, err := ffmpeg.Start(prog, args, r.events())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	return &r
}

func readFixture(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestProgressFixtures(t *testing.T) {
	ms := time.Millisecond
	us := time.Microsecond
	// The expected values were read from the fixtures with awk.
	tests := []struct {
		dir, file string
		outTimes  []time.Duration
		frames    []int64
		lastSize  int64
		lastSpeed float64
	}{
		{"ffmpeg-5.1", "progress-av.txt",
			[]time.Duration{1 * ms, 521 * ms, 1045 * ms, 1521 * ms, 2044 * ms, 2554 * ms, 3018 * ms},
			[]int64{1, 14, 26, 39, 51, 64, 75}, 183901, 1},
		{"ffmpeg-5.1", "progress-audio.txt",
			[]time.Duration{0, 366780 * us, 889229 * us, 1385556 * us, 2012494 * us},
			[]int64{0, 0, 0, 0, 0}, 16527, 1},
		{"ffmpeg-9.0", "progress-av.txt",
			[]time.Duration{1137778 * us, 1648617 * us, 2136236 * us, 2647075 * us, 3000 * ms},
			[]int64{25, 38, 50, 63, 75}, 183942, 1.22},
		{"ffmpeg-9.0", "progress-audio.txt",
			[]time.Duration{889229 * us, 1359433 * us, 2000 * ms},
			[]int64{0, 0, 0}, 16526, 1.33},
	}
	for _, tt := range tests {
		for _, crlf := range []bool{false, true} {
			t.Run(tt.dir+"/"+tt.file+map[bool]string{false: "", true: "/CRLF"}[crlf], func(t *testing.T) {
				out := readFixture(t, tt.dir, tt.file)
				if crlf {
					out = strings.ReplaceAll(out, "\n", "\r\n")
				}
				got := replay(t, out, "").progress
				if len(got) != len(tt.outTimes) {
					t.Fatalf("got %d updates, want %d: %+v", len(got), len(tt.outTimes), got)
				}
				for i, p := range got {
					if p.OutTime != tt.outTimes[i] || p.Frame != tt.frames[i] || p.End != (i == len(got)-1) {
						t.Errorf("update %d = %+v, want out time %v, frame %d", i, p, tt.outTimes[i], tt.frames[i])
					}
				}
				if last := got[len(got)-1]; last.TotalSize != tt.lastSize || last.Speed != tt.lastSpeed {
					t.Errorf("last update = %+v, want size %d, speed %v", last, tt.lastSize, tt.lastSpeed)
				}
			})
		}
	}
}

func TestProgressOddValues(t *testing.T) {
	out := strings.Join([]string{
		// Before the first packet, FFmpeg reports N/A.
		"frame=0", "fps=0.00", "total_size=N/A", "out_time_us=N/A", "out_time_ms=N/A", "out_time=N/A", "speed=N/A", "progress=continue",
		// Only out_time_ms, which is in microseconds despite its name.
		"out_time_ms=1500000", "speed=   2x", "progress=continue",
		// FFmpeg writes out_time_us first; it wins over out_time_ms.
		"out_time_us=2500000", "out_time_ms=999", "progress=continue",
		// Negative while seeking.
		"out_time_us=-23000", "out_time_ms=-23000", "progress=continue",
		"garbage line", "out_time_us=7000000", "fps=29.97", "progress=end", "",
	}, "\n")
	got := replay(t, out, "").progress
	want := []ffmpeg.Progress{
		{},
		{OutTime: 1500 * time.Millisecond, Speed: 2},
		{OutTime: 2500 * time.Millisecond},
		{},
		{OutTime: 7 * time.Second, FPS: 29.97, End: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestLogLines(t *testing.T) {
	stderr := readFixture(t, "ffmpeg-9.0", "stderr-av.txt")
	// FFmpeg rewrites a status line with \r; each rewrite is its own line.
	r := replay(t, "progress=end\n", stderr+"frame=  1 fps=0\rframe=  2 fps=0\r\n\nlast line")
	if len(r.log) < 5 || !strings.HasPrefix(r.log[0], "Input #0, lavfi") {
		t.Fatalf("log = %q", r.log)
	}
	if got := r.log[len(r.log)-3:]; !slices.Equal(got, []string{"frame=  1 fps=0", "frame=  2 fps=0", "last line"}) {
		t.Errorf("last lines = %q", got)
	}
	for _, line := range r.log {
		if strings.ContainsAny(line, "\r\n") || strings.TrimSpace(line) == "" {
			t.Errorf("bad line %q", line)
		}
	}
}

func TestExitZeroWithoutEndIsFailure(t *testing.T) {
	// What FFmpeg 9 does when the output exists and -n is set.
	dir := t.TempDir()
	stderr := filepath.Join(dir, "stderr")
	os.WriteFile(stderr, []byte("File 'x.mkv' already exists. Exiting.\nError opening output file x.mkv.\n"), 0o644)
	t.Setenv("FFTUI_STDOUT", "")
	t.Setenv("FFTUI_STDERR", stderr)
	prog, args := helper(t, "replay")
	p, err := ffmpeg.Start(prog, args, ffmpeg.Events{})
	if err != nil {
		t.Fatal(err)
	}
	var runErr *ffmpeg.RunError
	if err := p.Wait(); !errors.As(err, &runErr) || runErr.Stderr != "File 'x.mkv' already exists. Exiting." {
		t.Errorf("Wait = %v, want a failure saying the file exists", err)
	}
}

func TestStopSendsQ(t *testing.T) {
	prog, args := helper(t, "wait-q")
	var r recorder
	p, err := ffmpeg.Start(prog, args, r.events())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	p.Stop(30 * time.Second)
	p.Stop(30 * time.Second) // a second Stop does nothing
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait = %v, want a clean exit after q", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("took %v; the process was killed instead of finishing", took)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.progress) != 2 || !r.progress[1].End || r.progress[1].Frame != 12 {
		t.Errorf("progress = %+v, want the update written after q", r.progress)
	}
}

func TestStopKillsAfterGrace(t *testing.T) {
	prog, args := helper(t, "ignore-q")
	p, err := ffmpeg.Start(prog, args, ffmpeg.Events{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	p.Stop(200 * time.Millisecond)
	err = p.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Wait = %v, want the kill reported", err)
	}
	if took := time.Since(start); took < 200*time.Millisecond || took > 10*time.Second {
		t.Errorf("killed after %v, want just after the 200ms grace", took)
	}
}

func TestFailureReportsLastLogLine(t *testing.T) {
	prog, args := helper(t, "fail")
	p, err := ffmpeg.Start(prog, args, ffmpeg.Events{})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Wait()
	var runErr *ffmpeg.RunError
	if !errors.As(err, &runErr) || runErr.Stderr != "out.mkv: Permission denied" {
		t.Fatalf("Wait = %v", err)
	}
	// Stop after exit must not panic or block.
	p.Stop(time.Millisecond)

	if _, err := ffmpeg.Start(filepath.Join(t.TempDir(), "no-ffmpeg"), nil, ffmpeg.Events{}); err == nil {
		t.Error("starting a missing program did not fail")
	}
}

// TestChildDiesWithFFTUI checks that an encode does not outlive fftui when
// fftui exits without stopping it, as when it crashes or is killed.
func TestChildDiesWithFFTUI(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("only Linux and Windows can tie a child's life to its parent")
	}
	beat := filepath.Join(t.TempDir(), "heartbeat")
	t.Setenv("FFTUI_HEARTBEAT", beat)
	prog, args := helper(t, "parent")
	if out, err := exec.Command(prog, args...).CombinedOutput(); err != nil {
		t.Fatalf("parent: %v\n%s", err, out)
	}
	size := func() int64 {
		fi, err := os.Stat(beat)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	// Give the kill a moment, then check that the heartbeat stopped.
	time.Sleep(200 * time.Millisecond)
	before := size()
	time.Sleep(500 * time.Millisecond)
	if after := size(); after != before {
		t.Errorf("the child kept running after its parent exited (heartbeat grew from %d to %d bytes)", before, after)
	}
}

func TestJobArgs(t *testing.T) {
	tests := []struct {
		args, want []string
	}{
		{[]string{"-i", "in.mp4", "out.mkv"}, []string{"-hide_banner", "-nostats", "-progress", "pipe:1", "-n", "-i", "in.mp4", "out.mkv"}},
		{[]string{"-y", "-i", "in.mp4", "out.mkv"}, []string{"-hide_banner", "-nostats", "-progress", "pipe:1", "-y", "-i", "in.mp4", "out.mkv"}},
		{[]string{"-i", "in.mp4", "-n", "out.mkv"}, []string{"-hide_banner", "-nostats", "-progress", "pipe:1", "-i", "in.mp4", "-n", "out.mkv"}},
		// "-y" inside a value is not the -y option.
		{[]string{"-i", "in.mp4", "-metadata", "title=-y", "out.mkv"}, []string{"-hide_banner", "-nostats", "-progress", "pipe:1", "-n", "-i", "in.mp4", "-metadata", "title=-y", "out.mkv"}},
	}
	for _, tt := range tests {
		if got := ffmpeg.JobArgs(tt.args); !slices.Equal(got, tt.want) {
			t.Errorf("JobArgs(%q) = %q\nwant %q", tt.args, got, tt.want)
		}
	}
}
