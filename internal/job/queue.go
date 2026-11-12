// Package job runs FFmpeg encodes as a queue: it starts as many at once as
// the jobs setting allows, records their progress and logs, and stops them.
package job

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

// State is where a job is in its life.
type State int

const (
	Queued State = iota
	Running
	Done
	Failed
	Cancelled
)

func (s State) String() string {
	return [...]string{"queued", "running", "done", "failed", "cancelled"}[s]
}

// Finished reports whether the job will not change again.
func (s State) Finished() bool { return s >= Done }

// Spec says what a job runs.
type Spec struct {
	// Name is what the job list shows, usually the output file.
	Name    string
	Program string // the ffmpeg to run
	// Steps are FFmpeg runs, in order, each without the arguments JobArgs
	// adds. A step starts only if the one before it succeeded. Two-pass
	// encodes have two; most jobs have one.
	Steps [][]string
	// Duration is how long the output will be, for the percentage. Zero
	// means unknown.
	Duration time.Duration
	// Output is the file the job writes. If the job fails and the file did
	// not exist when the job started, the half-written file is deleted.
	Output string
	// Temp are glob patterns of files the job leaves behind, such as
	// two-pass logs, deleted once it ends however it ends.
	Temp []string
	// Retry is the same job with a software encoder, offered when a job
	// using a hardware encoder fails. Nil means there is none.
	Retry *Spec
}

// Job is a snapshot of one job.
type Job struct {
	ID int
	Spec
	State State
	// Step is the index in Steps of the running or last run step.
	Step     int
	Progress ffmpeg.Progress // of the current step
	// Log holds the last MaxLog lines FFmpeg wrote; Dropped counts older
	// lines that no longer fit.
	Log     []string
	Dropped int
	// Err says why a Failed job failed.
	Err error
	// Stopping is set once a running job has been asked to stop.
	Stopping bool
	Started  time.Time
	Finished time.Time
}

// MaxLog is how many log lines a job keeps.
const MaxLog = 2000

// StopGrace is how long FFmpeg gets to finish its files after being asked
// to stop, before it is killed.
const StopGrace = 15 * time.Second

// Process is a running encode. *ffmpeg.Process implements it.
type Process interface {
	Stop(grace time.Duration)
	Wait() error
}

// StartFunc starts step of spec. It must not call ev before it returns.
type StartFunc func(spec Spec, step int, ev ffmpeg.Events) (Process, error)

// StartFFmpeg starts a step with ffmpeg.Start and ffmpeg.JobArgs.
func StartFFmpeg(spec Spec, step int, ev ffmpeg.Events) (Process, error) {
	return ffmpeg.Start(spec.Program, ffmpeg.JobArgs(spec.Steps[step]), ev)
}

// Queue runs jobs. Its methods are safe to call from any goroutine.
type Queue struct {
	start   StartFunc
	changed chan struct{}
	running sync.WaitGroup

	mu    sync.Mutex
	limit int
	jobs  []*entry
}

type entry struct {
	Job
	proc Process
	// outputExisted is set when Output was already there at the start, so
	// it is never deleted.
	outputExisted bool
}

// NewQueue returns a queue that runs up to limit jobs at once, at least one,
// starting each with start.
func NewQueue(limit int, start StartFunc) *Queue {
	return &Queue{
		start:   start,
		changed: make(chan struct{}, 1),
		limit:   max(limit, 1),
	}
}

// Limit is how many jobs run at once.
func (q *Queue) Limit() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.limit
}

// SetLimit changes how many jobs run at once, at least one, and starts
// queued jobs if there is now room. Running jobs keep running when the limit
// drops; no new ones start until fewer than the limit are left.
func (q *Queue) SetLimit(limit int) {
	q.mu.Lock()
	q.limit = max(limit, 1)
	q.startNext()
	q.mu.Unlock()
	q.notify()
}

// Changed receives a value after jobs change. Changes that happen while
// nobody is receiving are merged into one.
func (q *Queue) Changed() <-chan struct{} { return q.changed }

func (q *Queue) notify() {
	select {
	case q.changed <- struct{}{}:
	default:
	}
}

// Add queues a job and starts it if there is room. It returns the job's ID.
func (q *Queue) Add(spec Spec) int {
	q.mu.Lock()
	id := len(q.jobs) + 1
	q.jobs = append(q.jobs, &entry{Job: Job{ID: id, Spec: spec}})
	q.startNext()
	q.mu.Unlock()
	q.notify()
	return id
}

// startNext starts queued jobs while fewer than limit are running. q.mu must
// be held.
func (q *Queue) startNext() {
	running := 0
	for _, e := range q.jobs {
		if e.State == Running {
			running++
		}
	}
	for _, e := range q.jobs {
		if running >= q.limit {
			return
		}
		if e.State == Queued {
			q.run(e)
			running++
		}
	}
}

// run starts e's first step. q.mu must be held.
func (q *Queue) run(e *entry) {
	e.State, e.Started = Running, time.Now()
	if e.Output != "" {
		_, err := os.Lstat(e.Output)
		e.outputExisted = err == nil
	}
	q.step(e)
}

// step starts e.Step. q.mu must be held.
func (q *Queue) step(e *entry) {
	e.Progress = ffmpeg.Progress{}
	proc, err := q.start(e.Spec, e.Step, ffmpeg.Events{
		Progress: func(p ffmpeg.Progress) {
			q.mu.Lock()
			e.Progress = p
			q.mu.Unlock()
			q.notify()
		},
		Log: func(line string) {
			q.mu.Lock()
			if len(e.Log) == MaxLog {
				// Drop the oldest tenth at once rather than one line per
				// new line, so a chatty FFmpeg does not copy the log on
				// every write.
				n := MaxLog / 10
				e.Log = append(e.Log[:0], e.Log[n:]...)
				e.Dropped += n
			}
			e.Log = append(e.Log, line)
			q.mu.Unlock()
			q.notify()
		},
	})
	if err != nil {
		q.finish(e, Failed, err)
		return
	}
	e.proc = proc
	q.running.Go(func() {
		err := proc.Wait()
		q.mu.Lock()
		switch {
		case e.Stopping:
			// FFmpeg exits cleanly after q, so a stop counts as a cancel
			// whatever its status.
			q.finish(e, Cancelled, nil)
		case err != nil:
			q.finish(e, Failed, err)
		case e.Step+1 < len(e.Steps):
			e.Step++
			q.step(e)
		default:
			q.finish(e, Done, nil)
		}
		q.startNext()
		q.mu.Unlock()
		q.notify()
	})
}

// finish ends e and removes the files it should not leave. q.mu must be
// held; the files are few, so removing them under it is fine.
func (q *Queue) finish(e *entry, state State, err error) {
	e.State, e.Err, e.Finished, e.proc = state, err, time.Now(), nil
	for _, pattern := range e.Temp {
		paths, _ := filepath.Glob(pattern)
		for _, p := range paths {
			_ = os.Remove(p)
		}
	}
	if state == Failed && e.Output != "" && !e.outputExisted {
		_ = os.Remove(e.Output)
	}
}

// Cancel stops a job: a queued job is dropped, and a running one is asked to
// finish its files and stop. Cancelling a finished job does nothing.
func (q *Queue) Cancel(id int) {
	q.mu.Lock()
	for _, e := range q.jobs {
		if e.ID == id {
			e.cancel()
		}
	}
	q.mu.Unlock()
	q.notify()
}

// cancel stops e. q.mu must be held.
func (e *entry) cancel() {
	switch e.State {
	case Queued:
		e.State, e.Finished = Cancelled, time.Now()
	case Running:
		if !e.Stopping && e.proc != nil {
			e.Stopping = true
			e.proc.Stop(StopGrace)
		}
	}
}

// Jobs returns a snapshot of every job, in the order they were added.
func (q *Queue) Jobs() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	jobs := make([]Job, len(q.jobs))
	for i, e := range q.jobs {
		jobs[i] = e.Job
		jobs[i].Log = append([]string(nil), e.Log...)
	}
	return jobs
}

// Active counts the jobs that are queued or running.
func (q *Queue) Active() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, e := range q.jobs {
		if !e.State.Finished() {
			n++
		}
	}
	return n
}

// StopAll cancels every job and waits until no FFmpeg is running. Each gets
// StopGrace to finish its files before it is killed.
func (q *Queue) StopAll() {
	// One pass under the lock, so a job that finishes meanwhile cannot start
	// a queued one.
	q.mu.Lock()
	for _, e := range q.jobs {
		e.cancel()
	}
	q.mu.Unlock()
	q.notify()
	q.running.Wait()
}
