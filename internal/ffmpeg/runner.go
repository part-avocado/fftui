// Package ffmpeg finds the ffmpeg and ffprobe programs, detects what the
// local FFmpeg build supports, and reads media information with ffprobe. It is
// the only package in fftui that starts processes.
package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner runs a program to completion and returns its standard output. The UI
// and tests swap in a fake Runner so they never start real processes.
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// RunnerFunc adapts a function to the Runner interface.
type RunnerFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

func (f RunnerFunc) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

// Exec is the Runner that starts real processes.
type Exec struct{}

// Output runs name with args. Standard input is the null device, so the child
// never reads keystrokes meant for the TUI. When the program fails, the error
// includes the line of standard error that says why.
func (Exec) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, &RunError{Program: filepath.Base(name), Err: err, Stderr: Reason(splitLines(stderr.String()))}
	}
	return out, nil
}

// RunError reports a program that could not start or exited with an error.
type RunError struct {
	Program string
	Err     error
	// Stderr is the line of the program's standard error that best says
	// why it failed, chosen by Reason.
	Stderr string
}

func (e *RunError) Error() string {
	if e.Stderr != "" {
		return e.Program + ": " + e.Stderr
	}
	return fmt.Sprintf("%s: %v", e.Program, e.Err)
}

func (e *RunError) Unwrap() error { return e.Err }

// splitLines splits FFmpeg output into lines, accepting both \n and the \r\n
// that Windows builds write.
func splitLines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// Tools is a working FFmpeg installation: where its programs are, what they
// support, and the Runner that starts them.
type Tools struct {
	Binaries
	Caps   Capabilities
	Runner Runner
}

// Find locates ffmpeg and ffprobe and detects what they support.
func Find(ctx context.Context, s Search, r Runner) (*Tools, error) {
	bins, err := Locate(s)
	if err != nil {
		return nil, err
	}
	caps, err := Detect(ctx, r, bins.FFmpeg)
	if err != nil {
		return nil, err
	}
	return &Tools{Binaries: bins, Caps: caps, Runner: r}, nil
}
