package job_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/job"
)

// fakeProc is an encode the test finishes by hand.
type fakeProc struct {
	ev      ffmpeg.Events
	stopped chan time.Duration
	exit    chan error
}

func (p *fakeProc) Stop(grace time.Duration) { p.stopped <- grace }
func (p *fakeProc) Wait() error              { return <-p.exit }

// fakes starts fake processes and records them by job name.
type fakes struct {
	mu      sync.Mutex
	procs   map[string]*fakeProc
	started []string
	fail    map[string]error
}

func newFakes() *fakes {
	return &fakes{procs: map[string]*fakeProc{}, fail: map[string]error{}}
}

// start records step 0 of a job under its name and later steps as
// "name/step".
func (f *fakes) start(spec job.Spec, step int, ev ffmpeg.Events) (job.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := spec.Name
	if step > 0 {
		name = fmt.Sprintf("%s/%d", spec.Name, step)
	}
	f.started = append(f.started, name)
	if err := f.fail[name]; err != nil {
		return nil, err
	}
	p := &fakeProc{ev: ev, stopped: make(chan time.Duration, 1), exit: make(chan error)}
	f.procs[name] = p
	return p, nil
}

func (f *fakes) proc(name string) *fakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[name]
}

func (f *fakes) startedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.started)
}

// waitFor polls until the queue reaches a condition; jobs finish on their
// own goroutines.
func waitFor(t *testing.T, q *job.Queue, what string, ok func([]job.Job) bool) []job.Job {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if jobs := q.Jobs(); ok(jobs) {
			return jobs
		}
	}
	t.Fatalf("timed out waiting for %s: %+v", what, q.Jobs())
	return nil
}

func states(jobs []job.Job) string {
	s := ""
	for _, j := range jobs {
		s += fmt.Sprintf("%s=%s ", j.Name, j.State)
	}
	return s
}

func stateIs(want string) func([]job.Job) bool {
	return func(jobs []job.Job) bool { return states(jobs) == want }
}

func TestQueueRunsUpToLimit(t *testing.T) {
	f := newFakes()
	q := job.NewQueue(2, f.start)
	for _, name := range []string{"a", "b", "c", "d"} {
		q.Add(job.Spec{Name: name})
	}
	if got := states(q.Jobs()); got != "a=running b=running c=queued d=queued " {
		t.Fatalf("states = %s", got)
	}
	if q.Active() != 4 {
		t.Errorf("Active = %d, want 4", q.Active())
	}

	// b finishing makes room for c, in the order jobs were added.
	f.proc("b").exit <- nil
	waitFor(t, q, "c to start", stateIs("a=running b=done c=running d=queued "))
	f.proc("a").exit <- errors.New("boom")
	waitFor(t, q, "d to start", stateIs("a=failed b=done c=running d=running "))

	jobs := q.Jobs()
	if jobs[0].Err == nil || jobs[0].Err.Error() != "boom" || jobs[1].Err != nil {
		t.Errorf("errors = %v, %v", jobs[0].Err, jobs[1].Err)
	}
	if jobs[1].Started.IsZero() || jobs[1].Finished.Before(jobs[1].Started) || !jobs[3].Finished.IsZero() {
		t.Errorf("times = %+v", jobs)
	}
	if got := f.startedNames(); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("started %q", got)
	}
}

func TestQueueSetLimit(t *testing.T) {
	f := newFakes()
	q := job.NewQueue(1, f.start)
	for _, name := range []string{"a", "b", "c", "d"} {
		q.Add(job.Spec{Name: name})
	}
	// Raising the limit starts queued jobs at once.
	q.SetLimit(3)
	if got := states(q.Jobs()); got != "a=running b=running c=running d=queued " {
		t.Fatalf("states = %s", got)
	}
	// Lowering it leaves running jobs alone, and starts nothing until
	// fewer than the limit run.
	q.SetLimit(1)
	f.proc("a").exit <- nil
	waitFor(t, q, "a to finish", stateIs("a=done b=running c=running d=queued "))
	f.proc("b").exit <- nil
	waitFor(t, q, "b to finish", stateIs("a=done b=done c=running d=queued "))
	f.proc("c").exit <- nil
	waitFor(t, q, "d to start", stateIs("a=done b=done c=done d=running "))
	if q.Limit() != 1 {
		t.Errorf("Limit = %d", q.Limit())
	}
	q.SetLimit(0)
	if q.Limit() != 1 {
		t.Errorf("Limit = %d after SetLimit(0), want 1", q.Limit())
	}
}

func TestQueueLimitBelowOne(t *testing.T) {
	f := newFakes()
	q := job.NewQueue(0, f.start)
	q.Add(job.Spec{Name: "a"})
	q.Add(job.Spec{Name: "b"})
	if got := states(q.Jobs()); got != "a=running b=queued " {
		t.Errorf("states = %s, want one at a time", got)
	}
}

func TestQueueStartFailure(t *testing.T) {
	f := newFakes()
	f.fail["a"] = errors.New("exec: no such file")
	q := job.NewQueue(1, f.start)
	q.Add(job.Spec{Name: "a"})
	q.Add(job.Spec{Name: "b"})
	// A job that cannot start fails at once and frees its slot.
	if got := states(q.Jobs()); got != "a=failed b=running " {
		t.Errorf("states = %s", got)
	}
}

func TestQueueCancel(t *testing.T) {
	f := newFakes()
	q := job.NewQueue(1, f.start)
	a := q.Add(job.Spec{Name: "a"})
	b := q.Add(job.Spec{Name: "b"})
	c := q.Add(job.Spec{Name: "c"})

	// A queued job is dropped without ever starting.
	q.Cancel(b)
	if got := states(q.Jobs()); got != "a=running b=cancelled c=queued " {
		t.Fatalf("states = %s", got)
	}

	// A running job is asked to stop and stays running until FFmpeg exits.
	q.Cancel(a)
	q.Cancel(a) // a second cancel does not stop it twice
	if grace := <-f.proc("a").stopped; grace != job.StopGrace {
		t.Errorf("grace = %v", grace)
	}
	select {
	case <-f.proc("a").stopped:
		t.Error("stopped twice")
	default:
	}
	if j := q.Jobs()[0]; j.State != job.Running || !j.Stopping {
		t.Errorf("a = %s, stopping %t", j.State, j.Stopping)
	}

	// FFmpeg exits cleanly after q; the job still counts as cancelled.
	f.proc("a").exit <- nil
	waitFor(t, q, "c to start", stateIs("a=cancelled b=cancelled c=running "))
	if got := f.startedNames(); !slices.Equal(got, []string{"a", "c"}) {
		t.Errorf("started %q, want b skipped", got)
	}

	f.proc("c").exit <- nil
	waitFor(t, q, "c to finish", stateIs("a=cancelled b=cancelled c=done "))
	q.Cancel(c) // finished: nothing happens
	if got := states(q.Jobs()); got != "a=cancelled b=cancelled c=done " || q.Active() != 0 {
		t.Errorf("states = %s, active %d", got, q.Active())
	}
}

func TestQueueProgressAndLog(t *testing.T) {
	f := newFakes()
	q := job.NewQueue(1, f.start)
	q.Add(job.Spec{Name: "a"})
	// Drain the Add notification.
	<-q.Changed()

	ev := f.proc("a").ev
	ev.Progress(ffmpeg.Progress{Frame: 12, OutTime: time.Second})
	select {
	case <-q.Changed():
	default:
		t.Error("progress did not notify")
	}
	for i := range job.MaxLog + 5 {
		ev.Log(fmt.Sprintf("line %d", i))
	}
	j := q.Jobs()[0]
	if j.Progress.Frame != 12 || j.Progress.OutTime != time.Second {
		t.Errorf("progress = %+v", j.Progress)
	}
	// The log keeps the newest lines and counts the ones it dropped.
	if last := j.Log[len(j.Log)-1]; last != fmt.Sprintf("line %d", job.MaxLog+4) {
		t.Errorf("last line = %q", last)
	}
	if len(j.Log) > job.MaxLog || j.Dropped+len(j.Log) != job.MaxLog+5 || j.Log[0] != fmt.Sprintf("line %d", j.Dropped) {
		t.Errorf("kept %d lines from %q, dropped %d", len(j.Log), j.Log[0], j.Dropped)
	}

	// A snapshot does not change when the job does, even when the queue
	// drops old lines from the live log.
	first, last := j.Log[0], j.Log[len(j.Log)-1]
	for range job.MaxLog / 5 {
		ev.Log("later")
	}
	if j.Log[0] != first || j.Log[len(j.Log)-1] != last {
		t.Errorf("snapshot changed to %q ... %q", j.Log[0], j.Log[len(j.Log)-1])
	}
	f.proc("a").exit <- nil
}

func TestQueueStopAll(t *testing.T) {
	f := newFakes()
	q := job.NewQueue(2, f.start)
	for _, name := range []string{"a", "b", "c"} {
		q.Add(job.Spec{Name: name})
	}
	done := make(chan struct{})
	go func() {
		q.StopAll()
		close(done)
	}()
	// Both running jobs are asked to stop, and StopAll waits for them.
	<-f.proc("a").stopped
	<-f.proc("b").stopped
	f.proc("a").exit <- nil
	select {
	case <-done:
		t.Fatal("StopAll returned while b was still running")
	case <-time.After(50 * time.Millisecond):
	}
	f.proc("b").exit <- errors.New("signal: killed")
	<-done
	// c never started, even though a slot opened while stopping.
	if got := states(q.Jobs()); got != "a=cancelled b=cancelled c=cancelled " {
		t.Errorf("states = %s", got)
	}
	if got := f.startedNames(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("started %q", got)
	}
}

func TestQueueSteps(t *testing.T) {
	f := newFakes()
	q := job.NewQueue(1, f.start)
	steps := [][]string{{"-pass", "1"}, {"-pass", "2"}}
	q.Add(job.Spec{Name: "two", Steps: steps})
	q.Add(job.Spec{Name: "next", Steps: steps[:1]})

	f.proc("two").ev.Progress(ffmpeg.Progress{Frame: 50, End: true})
	f.proc("two").exit <- nil
	// The second step starts in the same slot, with fresh progress, before
	// the next job.
	jobs := waitFor(t, q, "step 2", func(j []job.Job) bool { return j[0].Step == 1 })
	if jobs[0].State != job.Running || jobs[0].Progress != (ffmpeg.Progress{}) || jobs[1].State != job.Queued {
		t.Fatalf("after step 1: %+v", jobs)
	}
	f.proc("two/1").exit <- nil
	waitFor(t, q, "next to start", stateIs("two=done next=running "))
	if got := f.startedNames(); !slices.Equal(got, []string{"two", "two/1", "next"}) {
		t.Errorf("started %q", got)
	}
}

func TestQueueStepFailureSkipsTheRest(t *testing.T) {
	f := newFakes()
	q := job.NewQueue(1, f.start)
	q.Add(job.Spec{Name: "a", Steps: [][]string{{"1"}, {"2"}}})
	f.proc("a").exit <- errors.New("pass 1 failed")
	waitFor(t, q, "a to fail", stateIs("a=failed "))

	// Cancelling during the first step does not start the second.
	b := q.Add(job.Spec{Name: "b", Steps: [][]string{{"1"}, {"2"}}})
	q.Cancel(b)
	<-f.proc("b").stopped
	f.proc("b").exit <- nil
	waitFor(t, q, "b to cancel", stateIs("a=failed b=cancelled "))
	if got := f.startedNames(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("started %q", got)
	}
}

func TestQueueRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }

	f := newFakes()
	q := job.NewQueue(3, f.start)
	existing := write("existing.mkv")
	keepLog := write("other-pass-0.log")
	q.Add(job.Spec{Name: "new", Output: filepath.Join(dir, "new.mkv"), Temp: []string{filepath.Join(dir, "new-pass*")}})
	q.Add(job.Spec{Name: "existing", Output: existing})
	q.Add(job.Spec{Name: "cancelled", Output: filepath.Join(dir, "cancelled.mp4")})

	// FFmpeg writes the outputs and the pass logs, then every job ends.
	newOut := write("new.mkv")
	passLogs := []string{write("new-pass-0.log"), write("new-pass-0.log.mbtree")}
	cancelled := write("cancelled.mp4")
	f.proc("new").exit <- errors.New("exit status 1")
	f.proc("existing").exit <- errors.New("already exists")
	q.Cancel(3)
	<-f.proc("cancelled").stopped
	f.proc("cancelled").exit <- nil
	waitFor(t, q, "every job to end", stateIs("new=failed existing=failed cancelled=cancelled "))

	if exists(newOut) {
		t.Error("the half-written output of a failed job was kept")
	}
	if !exists(existing) {
		t.Error("a file that was there before the job was deleted")
	}
	if !exists(cancelled) {
		t.Error("a cancelled job's output was deleted; FFmpeg closed it properly")
	}
	for _, p := range passLogs {
		if exists(p) {
			t.Errorf("%s was not cleaned up", p)
		}
	}
	if !exists(keepLog) {
		t.Error("a file outside the Temp pattern was deleted")
	}
}
