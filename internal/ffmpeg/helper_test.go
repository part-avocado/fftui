package ffmpeg_test

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

// helper returns the program and arguments that run this test binary as a
// fake FFmpeg in the given mode. See TestHelperProcess.
func helper(t *testing.T, mode string) (string, []string) {
	t.Helper()
	t.Setenv("FFTUI_HELPER", mode)
	return os.Args[0], []string{"-test.run=^TestHelperProcess$"}
}

// TestHelperProcess is not a real test. Tests run the test binary as a child
// process that acts like FFmpeg, chosen by FFTUI_HELPER.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("FFTUI_HELPER")
	if mode == "" {
		t.Skip("helper process only")
	}
	block := "frame=10\nout_time_us=400000\nprogress=continue\n"
	switch mode {
	case "probe-fail":
		os.Stdout.WriteString("partial output")
		os.Stderr.WriteString("[mov,mp4] some warning\r\nthe real reason\r\n\r\n")
		os.Exit(1)

	case "replay":
		// Writes files to standard output and error, as FFmpeg would.
		for env, w := range map[string]*os.File{"FFTUI_STDOUT": os.Stdout, "FFTUI_STDERR": os.Stderr} {
			if path := os.Getenv(env); path != "" {
				data, err := os.ReadFile(path)
				if err != nil {
					os.Exit(2)
				}
				w.Write(data)
			}
		}
		os.Exit(0)

	case "fail":
		os.Stderr.WriteString("Input #0, lavfi\nout.mkv: Permission denied\r\n")
		os.Exit(1)

	case "wait-q":
		// Like FFmpeg: reads its input until q, then finishes the file.
		os.Stdout.WriteString(block)
		buf := make([]byte, 1)
		for {
			if _, err := os.Stdin.Read(buf); err != nil {
				os.Exit(3) // input closed without q
			}
			if buf[0] == 'q' {
				os.Stdout.WriteString("frame=12\nout_time_us=480000\nprogress=end\n")
				os.Exit(0)
			}
		}

	case "ignore-q":
		os.Stdout.WriteString(block)
		time.Sleep(time.Minute)
		os.Exit(4)

	case "parent":
		// Starts a heartbeat child, waits until it beats, then exits
		// without stopping it, as a crashed fftui would.
		os.Setenv("FFTUI_HELPER", "heartbeat")
		if _, err := ffmpeg.Start(os.Args[0], []string{"-test.run=^TestHelperProcess$"}, ffmpeg.Events{}); err != nil {
			os.Exit(5)
		}
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if fi, err := os.Stat(os.Getenv("FFTUI_HEARTBEAT")); err == nil && fi.Size() > 0 {
				os.Exit(0)
			}
		}
		os.Exit(6)

	case "heartbeat":
		// Appends a byte every 20ms for up to 15s, so a leaked child stops
		// by itself.
		f, err := os.OpenFile(os.Getenv("FFTUI_HEARTBEAT"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			os.Exit(7)
		}
		for range 15 * 50 {
			f.Write([]byte("."))
			time.Sleep(20 * time.Millisecond)
		}
		os.Exit(0)
	}
	io.WriteString(os.Stderr, "unknown helper mode "+mode)
	os.Exit(9)
}
