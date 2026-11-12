package ffmpeg_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

// TestRealFFmpeg finds the installed FFmpeg, detects it, and probes a clip it
// made. It is skipped when FFmpeg is missing, unless FFTUI_REQUIRE_FFMPEG is
// set, as it is in CI.
func TestRealFFmpeg(t *testing.T) {
	ctx := context.Background()
	tools, err := ffmpeg.Find(ctx, ffmpeg.Search{Dirs: ffmpeg.CommonDirs()}, ffmpeg.Exec{})
	if err != nil {
		if os.Getenv("FFTUI_REQUIRE_FFMPEG") == "" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	t.Logf("using %s (%s), version %s", tools.FFmpeg, tools.Source, tools.Caps.Version.String)
	if !tools.Caps.Version.AtLeast(4, 4) {
		t.Fatalf("FFmpeg %s is older than 4.4", tools.Caps.Version.String)
	}
	if len(tools.Caps.Encoders) < 50 || len(tools.Caps.Decoders) < 50 || len(tools.Caps.Muxers) < 50 {
		t.Errorf("only %d encoders, %d decoders, %d muxers", len(tools.Caps.Encoders), len(tools.Caps.Decoders), len(tools.Caps.Muxers))
	}

	// mpeg4 and aac are built into every FFmpeg, unlike libx264.
	clip := filepath.Join(t.TempDir(), "clip ü.mkv")
	gen := exec.Command(tools.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1",
		"-c:v", "mpeg4", "-c:a", "aac", "-metadata", "title=Integration", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("making the clip: %v\n%s", err, out)
	}

	m, err := tools.Probe(ctx, clip)
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != "matroska,webm" || m.Title != "Integration" || m.Size == 0 {
		t.Errorf("format %q, title %q, size %d", m.Format, m.Title, m.Size)
	}
	if d := m.Duration - time.Second; d < -100*time.Millisecond || d > 100*time.Millisecond {
		t.Errorf("duration = %v, want about 1s", m.Duration)
	}
	if len(m.Streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(m.Streams))
	}
	v, a := m.Streams[0], m.Streams[1]
	if v.Type != "video" || v.Codec != "mpeg4" || v.Width != 320 || v.Height != 240 || v.FrameRate != 25 {
		t.Errorf("video = %+v", v)
	}
	if a.Type != "audio" || a.Codec != "aac" || a.SampleRate != 48000 || a.Channels != 1 {
		t.Errorf("audio = %+v", a)
	}

	if _, err := tools.Probe(ctx, filepath.Join(t.TempDir(), "missing.mp4")); err == nil {
		t.Error("probing a missing file did not fail")
	}
}

// TestRealFFmpegJob runs encodes with the installed FFmpeg: one to the end,
// one cancelled partway, and one whose output already exists.
func TestRealFFmpegJob(t *testing.T) {
	ctx := context.Background()
	tools, err := ffmpeg.Find(ctx, ffmpeg.Search{Dirs: ffmpeg.CommonDirs()}, ffmpeg.Exec{})
	if err != nil {
		if os.Getenv("FFTUI_REQUIRE_FFMPEG") == "" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	dir := t.TempDir()

	full := filepath.Join(dir, "full ü.mkv")
	var r recorder
	p, err := ffmpeg.Start(tools.FFmpeg, ffmpeg.JobArgs([]string{
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=25:duration=2", "-c:v", "mpeg4", full,
	}), r.events())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	last := r.progress[len(r.progress)-1]
	if !last.End || last.Frame != 50 || last.OutTime < 1900*time.Millisecond || last.TotalSize == 0 {
		t.Errorf("last update = %+v, want the end of a 2s, 50-frame encode", last)
	}
	if !slices.ContainsFunc(r.log, func(l string) bool { return strings.HasPrefix(l, "Stream mapping:") }) {
		t.Errorf("log has no stream mapping: %q", r.log)
	}
	r.mu.Unlock()

	// -n from JobArgs: an existing output fails at once instead of waiting
	// for an answer to FFmpeg's overwrite question.
	// FFmpeg 9 exits with status 0 here, so this also checks that a run
	// without progress=end counts as failed.
	var again recorder
	p, err = ffmpeg.Start(tools.FFmpeg, ffmpeg.JobArgs([]string{"-f", "lavfi", "-i", "testsrc2=duration=1", full}), again.events())
	if err != nil {
		t.Fatal(err)
	}
	var runErr *ffmpeg.RunError
	if err := p.Wait(); !errors.As(err, &runErr) {
		t.Errorf("overwriting: err = %v, want a failure", err)
	}
	if !slices.ContainsFunc(again.log, func(l string) bool { return strings.Contains(l, "already exists") }) {
		t.Errorf("overwriting: log = %q, want FFmpeg's \"already exists\"", again.log)
	}

	// Cancel a real-time encode after about a second. MP4 only plays if
	// FFmpeg finished writing it, so a valid file shows q was honored.
	cancelled := filepath.Join(dir, "cancelled.mp4")
	started := make(chan struct{})
	var once sync.Once
	p, err = ffmpeg.Start(tools.FFmpeg, ffmpeg.JobArgs([]string{
		"-re", "-f", "lavfi", "-i", "testsrc2=size=160x120:rate=25:duration=60", "-c:v", "mpeg4", cancelled,
	}), ffmpeg.Events{Progress: func(p ffmpeg.Progress) {
		if p.OutTime >= time.Second {
			once.Do(func() { close(started) })
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("no progress after 30s")
	}
	stop := time.Now()
	p.Stop(20 * time.Second)
	if err := p.Wait(); err != nil {
		t.Fatalf("stopping: %v", err)
	}
	if took := time.Since(stop); took > 10*time.Second {
		t.Errorf("FFmpeg took %v to stop", took)
	}
	m, err := tools.Probe(ctx, cancelled)
	if err != nil {
		t.Fatalf("the cancelled file is not readable: %v", err)
	}
	if m.Duration < time.Second || m.Duration > 20*time.Second {
		t.Errorf("cancelled file is %v long, want a bit over 1s", m.Duration)
	}
}
