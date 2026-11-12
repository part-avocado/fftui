package ui_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/part-avocado/fftui/internal/cmdline"
	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/job"
	"github.com/part-avocado/fftui/internal/ui"
)

// fakeJobs starts encodes that the test drives by hand.
type fakeJobs struct {
	mu    sync.Mutex
	specs []job.Spec
	procs map[string]*fakeProc
}

type fakeProc struct {
	ev      ffmpeg.Events
	stopped chan struct{}
	once    sync.Once
	exit    chan error
}

func (p *fakeProc) Stop(time.Duration) { p.once.Do(func() { close(p.stopped) }) }
func (p *fakeProc) Wait() error        { return <-p.exit }

func newFakeJobs() *fakeJobs { return &fakeJobs{procs: map[string]*fakeProc{}} }

// start records step 0 of a job under its name and later steps as
// "name/step".
func (f *fakeJobs) start(spec job.Spec, step int, ev ffmpeg.Events) (job.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := spec.Name
	if step == 0 {
		f.specs = append(f.specs, spec)
	} else {
		name = fmt.Sprintf("%s/%d", name, step)
	}
	p := &fakeProc{ev: ev, stopped: make(chan struct{}), exit: make(chan error, 1)}
	f.procs[name] = p
	return p, nil
}

func (f *fakeJobs) proc(t *testing.T, name string) *fakeProc {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.procs[name]
	if p == nil {
		t.Fatalf("job %q never started", name)
	}
	return p
}

func stopped(p *fakeProc) bool {
	select {
	case <-p.stopped:
		return true
	default:
		return false
	}
}

// finish makes a fake encode exit and waits until the queue has seen it.
func finish(t *testing.T, q *job.Queue, p *fakeProc, err error) {
	t.Helper()
	before := q.Active()
	p.exit <- err
	for deadline := time.Now().Add(5 * time.Second); q.Active() == before; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the queue did not see the job finish")
		}
	}
}

// clock is a settable time for the elapsed and remaining times.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func assertMissing(t *testing.T, m tea.Model, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(screen(m), u) {
			t.Errorf("screen contains %q:\n%s", u, screen(m))
		}
	}
}

// commandApp starts the app with an FFmpeg command from the command line.
func commandApp(t *testing.T, command ...string) (tea.Model, *fakeJobs, *job.Queue, *clock) {
	t.Helper()
	f := newFakeJobs()
	q := job.NewQueue(1, f.start)
	c := &clock{t: time.Now()}
	m := newApp(t, appOpts{queue: q, command: command, now: c.now})
	return m, f, q, c
}

func TestCommandRunsAsJob(t *testing.T) {
	m, f, q, c := commandApp(t, "-i", "multi stream.mkv", "-c:v", "mpeg4", "out file.mkv")
	// The job runs the found ffmpeg with the arguments as given, and the
	// input's duration (3.026s in the ffprobe fixture) for the percentage.
	want := job.Spec{Name: "out file.mkv", Program: "/usr/bin/ffmpeg",
		Steps: [][]string{{"-i", "multi stream.mkv", "-c:v", "mpeg4", "out file.mkv"}}, Duration: 3026 * time.Millisecond}
	if len(f.specs) != 1 || f.specs[0].Name != want.Name || f.specs[0].Program != want.Program ||
		!slices.EqualFunc(f.specs[0].Steps, want.Steps, slices.Equal) || f.specs[0].Duration != want.Duration || f.specs[0].Output != "" {
		t.Fatalf("started %+v\nwant %+v", f.specs, want)
	}
	// The app opens on the Jobs screen.
	command := `ffmpeg -i 'multi stream.mkv' -c:v mpeg4 'out file.mkv'`
	if cmdline.Windows {
		command = `ffmpeg -i "multi stream.mkv" -c:v mpeg4 "out file.mkv"`
	}
	assertContains(t, m, "> running   out file.mkv  0%", command)

	p := f.proc(t, "out file.mkv")
	c.set(q.Jobs()[0].Started.Add(4 * time.Second))
	p.ev.Progress(ffmpeg.Progress{OutTime: 1513 * time.Millisecond, Speed: 2, TotalSize: 1572864})
	p.ev.Log("Stream mapping:")
	p.ev.Log("  Stream #0:0 -> #0:0 (h264 (native) -> mpeg4 (native))")
	m = send(m, ui.JobsChanged{})
	// Half done after 4s, so about 4s left.
	assertContains(t, m,
		"> running   out file.mkv  50%",
		"█", "░", " 50%",
		"0:00:02 of 0:00:03 • 2x • 1.5 MiB • 0:00:04 elapsed • about 0:00:04 left",
		"Stream mapping:",
		"Stream #0:0 -> #0:0 (h264 (native) -> mpeg4 (native))",
		"x cancel job",
	)

	p.ev.Progress(ffmpeg.Progress{OutTime: 3026 * time.Millisecond, TotalSize: 3145728, End: true})
	finish(t, q, p, nil)
	m = send(m, ui.JobsChanged{})
	assertContains(t, m, "> done      out file.mkv  took 0:00:00", "Done in 0:00:00 • 0:00:03 written • 3.0 MiB")
	assertMissing(t, m, "x cancel job", "elapsed")
}

func TestCommandWithUnknownDuration(t *testing.T) {
	// -t cuts the output, so the input's duration would give a wrong
	// percentage; the screen shows the time written instead.
	m, f, _, _ := commandApp(t, "-i", "multi stream.mkv", "-t", "1", "cut.mkv")
	if f.specs[0].Duration != 0 {
		t.Errorf("duration = %v, want unknown", f.specs[0].Duration)
	}
	f.proc(t, "cut.mkv").ev.Progress(ffmpeg.Progress{OutTime: 700 * time.Millisecond})
	m = send(m, ui.JobsChanged{})
	assertContains(t, m, "> running   cut.mkv  0:00:01", "0:00:01 written")
	assertMissing(t, m, "%", "left")
}

func TestCommandWaitsForFFmpeg(t *testing.T) {
	f := newFakeJobs()
	m := newApp(t, appOpts{queue: job.NewQueue(1, f.start), command: []string{"-i", "a.mp4", "b.mkv"}, find: notFound})
	assertContains(t, m, "Waiting for FFmpeg", "The Files screen explains")
	if len(f.specs) != 0 {
		t.Errorf("started %+v without FFmpeg", f.specs)
	}
}

func TestCancelJob(t *testing.T) {
	m, f, q, _ := commandApp(t, "-i", "song.mp3", "a.mkv")
	q.Add(job.Spec{Name: "b.mkv", Program: "ffmpeg", Steps: [][]string{{"-i", "song.mp3", "b.mkv"}}})
	m = send(m, ui.JobsChanged{})
	assertContains(t, m, "> running   a.mkv", "  queued    b.mkv")
	a := f.proc(t, "a.mkv")

	// Cancelling asks first, and n keeps the job running.
	m = send(m, char('x'))
	assertContains(t, m, "Cancel this job?", "y cancel it", "n keep running")
	m = send(m, char('n'))
	assertMissing(t, m, "Cancel this job?")
	if stopped(a) {
		t.Fatal("n stopped the job")
	}

	// y asks FFmpeg to stop; the job stays running until FFmpeg exits.
	m = send(m, char('x'), char('y'))
	if !stopped(a) {
		t.Fatal("y did not stop the job")
	}
	assertContains(t, m, "> running   a.mkv  stopping…", "Stopping…")
	assertMissing(t, m, "x cancel job")

	// A queued job is cancelled without starting.
	m = send(m, down)
	assertContains(t, m, "> queued    b.mkv", "allows 1 at once")
	m = send(m, char('x'), char('y'))
	assertContains(t, m, "> cancelled b.mkv", "Cancelled before it started.")

	finish(t, q, a, nil)
	m = send(m, ui.JobsChanged{})
	assertContains(t, m, "  cancelled a.mkv")
	if len(f.specs) != 1 {
		t.Errorf("started %d jobs, want only a.mkv", len(f.specs))
	}
}

func TestJobFinishesWhileAskingToCancel(t *testing.T) {
	m, f, q, _ := commandApp(t, "-i", "song.mp3", "a.mkv")
	m = send(m, char('x'))
	assertContains(t, m, "Cancel this job?")
	finish(t, q, f.proc(t, "a.mkv"), nil)
	m = send(m, ui.JobsChanged{})
	assertContains(t, m, "> done      a.mkv")
	assertMissing(t, m, "Cancel this job?", "y cancel it")
}

func TestFailedJob(t *testing.T) {
	m, f, q, _ := commandApp(t, "-i", "song.mp3", "/root/out.mkv")
	p := f.proc(t, "/root/out.mkv")
	p.ev.Log("Input #0, mp3, from 'song.mp3':")
	p.ev.Log("/root/out.mkv: Permission denied")
	finish(t, q, p, &ffmpeg.RunError{Program: "ffmpeg", Err: errors.New("exit status 1"), Stderr: "/root/out.mkv: Permission denied"})
	m = send(m, ui.JobsChanged{})
	assertContains(t, m,
		"> failed    /root/out.mkv  ffmpeg: /root/out.mkv: Permission denied",
		"Failed: ffmpeg: /root/out.mkv: Permission denied",
		"Input #0, mp3, from 'song.mp3':",
	)
}

func TestLogView(t *testing.T) {
	m, f, _, _ := commandApp(t, "-i", "song.mp3", "out.mkv")
	p := f.proc(t, "out.mkv")
	for i := range 60 {
		p.ev.Log(fmt.Sprintf("log line %02d", i))
	}
	m = send(m, ui.JobsChanged{}, char('l'))
	// It opens at the end.
	assertContains(t, m, "Log of out.mkv (60 lines)", "log line 59", "esc back")
	assertMissing(t, m, "log line 00", "> running")

	m = send(m, pgUp)
	assertMissing(t, m, "log line 59")
	m = send(m, tea.KeyPressMsg{Code: tea.KeyHome})
	assertContains(t, m, "log line 00")

	// While scrolled back, new lines do not move the view...
	p.ev.Log("log line 60")
	m = send(m, ui.JobsChanged{})
	assertContains(t, m, "log line 00")
	// ...and at the end the view follows them.
	m = send(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	p.ev.Log("log line 61")
	m = send(m, ui.JobsChanged{})
	assertContains(t, m, "log line 61", "(62 lines)")

	m = send(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	assertContains(t, m, "> running   out.mkv")
}

func TestQuitWithJobsRunning(t *testing.T) {
	m, f, q, _ := commandApp(t, "-i", "song.mp3", "out.mkv")
	m, cmd := m.Update(char('q'))
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("q quit at once with a job running")
		}
	}
	assertContains(t, m, "1 job is not finished. Quit and stop it?")
	m = send(m, char('n'))
	assertMissing(t, m, "Quit and stop it?")

	asking, _ := m.Update(char('q'))
	if _, cmd = asking.Update(char('y')); cmd == nil {
		t.Fatal("y did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("y did not quit")
	}

	// Once the job is done, q quits without asking.
	finish(t, q, f.proc(t, "out.mkv"), nil)
	if _, cmd = m.Update(char('q')); cmd == nil {
		t.Fatal("q did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q did not quit")
	}
}
