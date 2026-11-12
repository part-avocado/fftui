package ffmpeg

import (
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// JobArgs returns the arguments that run an encode with args: FFmpeg writes
// progress to standard output instead of stats to the log, and refuses to
// overwrite an existing file unless args has -y. Without -n, FFmpeg would
// wait forever for an answer to its overwrite question.
func JobArgs(args []string) []string {
	out := []string{"-hide_banner", "-nostats", "-progress", "pipe:1"}
	if !slices.Contains(args, "-y") && !slices.Contains(args, "-n") {
		out = append(out, "-n")
	}
	return append(out, args...)
}

// Events receives what a Process reports. Both functions are called from the
// Process's own goroutines, and either may be nil.
type Events struct {
	Progress func(Progress)
	// Log receives each line FFmpeg writes to standard error.
	Log func(line string)
}

// Process is a running FFmpeg encode, started with Start.
type Process struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{}
	err   error

	mu       sync.Mutex
	tail     []string // the last lines written to standard error
	ended    bool     // FFmpeg wrote progress=end
	stopping bool
}

// errNoEnd reports an FFmpeg that exited with status 0 without finishing,
// which FFmpeg 9 does when it refuses to overwrite a file.
var errNoEnd = errors.New("exited without finishing")

// Start runs program with args, which usually come from JobArgs. Its
// standard input is a pipe so Stop can ask it to finish. On Linux and
// Windows the process is killed if fftui dies without stopping it.
func Start(program string, args []string, ev Events) (*Process, error) {
	cmd := exec.Command(program, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := startTied(cmd); err != nil {
		return nil, &RunError{Program: filepath.Base(program), Err: err}
	}

	p := &Process{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	var readers sync.WaitGroup
	readers.Go(func() {
		readProgress(stdout, func(pr Progress) {
			if pr.End {
				p.mu.Lock()
				p.ended = true
				p.mu.Unlock()
			}
			if ev.Progress != nil {
				ev.Progress(pr)
			}
		})
	})
	readers.Go(func() {
		scanLines(stderr, '\r', func(line string) {
			p.mu.Lock()
			if len(p.tail) == tailLines {
				p.tail = p.tail[1:]
			}
			p.tail = append(p.tail, line)
			p.mu.Unlock()
			if ev.Log != nil {
				ev.Log(line)
			}
		})
	})
	go func() {
		// Wait closes the pipes, so it must come after all reads.
		readers.Wait()
		err := cmd.Wait()
		p.mu.Lock()
		if err == nil && !p.ended {
			err = errNoEnd
		}
		if err != nil {
			p.err = &RunError{Program: filepath.Base(program), Err: err, Stderr: Reason(p.tail)}
		}
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

// Stop asks FFmpeg to finish by sending q, which it answers by closing its
// outputs so they stay playable. If it has not exited after grace, it is
// killed. Stop returns at once; use Wait to know when the process is gone.
func (p *Process) Stop(grace time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return
	}
	p.stopping = true
	// This fails only if FFmpeg has already exited or closed its input.
	_, _ = io.WriteString(p.stdin, "q")
	time.AfterFunc(grace, func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
		}
	})
}

// Wait waits for the process to exit. It returns nil when FFmpeg succeeded,
// including after Stop, and a *RunError with the log line that says why when it
// failed or was killed. Success means exit status 0 and a final progress=end:
// some FFmpeg versions exit with 0 after an error.
func (p *Process) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}
