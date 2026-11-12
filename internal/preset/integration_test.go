package preset_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/job"
	"github.com/part-avocado/fftui/internal/preset"
)

// TestRealFFmpeg runs every preset through the job queue with the installed
// FFmpeg, then checks the output with ffprobe. It is skipped when FFmpeg is
// missing, unless FFTUI_REQUIRE_FFMPEG is set, as it is in CI.
func TestRealFFmpeg(t *testing.T) {
	ctx := context.Background()
	tools, err := ffmpeg.Find(ctx, ffmpeg.Search{Dirs: ffmpeg.CommonDirs()}, ffmpeg.Exec{})
	if err != nil {
		if os.Getenv("FFTUI_REQUIRE_FFMPEG") == "" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	t.Logf("using %s, version %s", tools.FFmpeg, tools.Caps.Version.String)
	dir := t.TempDir()

	// 3s of 640x360 video, stereo audio, and English subtitles, made with
	// encoders every FFmpeg has.
	srt := filepath.Join(dir, "subs.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:02,500\nHello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "in clip ü.mkv")
	gen := exec.Command(tools.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=3",
		"-i", srt, "-map", "0", "-map", "1", "-map", "2", "-ac", "2",
		"-c:v", "mpeg4", "-q:v", "3", "-c:a", "aac", "-c:s", "srt", "-metadata:s:s:0", "language=eng", input)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("making the input: %v\n%s", err, out)
	}
	media, err := tools.Probe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}

	type stream struct{ typ, codec string }
	tests := []struct {
		id      string
		set     preset.Values
		streams []stream
		check   func(t *testing.T, m *ffmpeg.Media, v preset.Values)
	}{
		{id: "convert", streams: []stream{{"video", "h264"}, {"audio", "aac"}, {"subtitle", "mov_text"}}},
		{id: "remux", streams: []stream{{"video", "mpeg4"}, {"audio", "aac"}, {"subtitle", "mov_text"}}},
		{id: "compress", streams: []stream{{"video", "h264"}, {"audio", "aac"}},
			check: func(t *testing.T, m *ffmpeg.Media, v preset.Values) {
				mb, _ := strconv.ParseFloat(v["size"], 64)
				if limit := int64(mb * 1e6 * 1.1); m.Size > limit {
					t.Errorf("%d bytes, want at most %d for %s MB", m.Size, limit, v["size"])
				}
			}},
		{id: "audio", streams: []stream{{"audio", "mp3"}}},
		{id: "trim", set: preset.Values{"start": "1", "end": "2.5"}, streams: []stream{{"video", "mpeg4"}, {"audio", "aac"}, {"subtitle", "subrip"}},
			check: func(t *testing.T, m *ffmpeg.Media, _ preset.Values) {
				// A fast cut starts at a keyframe up to half a second early.
				if m.Duration < 1400*time.Millisecond || m.Duration > 2100*time.Millisecond {
					t.Errorf("duration %v, want 1.5s to 2s", m.Duration)
				}
			}},
		{id: "trim", set: preset.Values{"start": "1", "end": "2.5", "mode": "exact"}, streams: []stream{{"video", "h264"}, {"audio", "aac"}, {"subtitle", "subrip"}},
			check: func(t *testing.T, m *ffmpeg.Media, _ preset.Values) {
				if d := m.Duration - 1500*time.Millisecond; d < -100*time.Millisecond || d > 100*time.Millisecond {
					t.Errorf("duration %v, want 1.5s", m.Duration)
				}
			}},
		{id: "resize", set: preset.Values{"height": "240"}, streams: []stream{{"video", "h264"}, {"audio", "aac"}, {"subtitle", "mov_text"}},
			check: func(t *testing.T, m *ffmpeg.Media, _ preset.Values) {
				if v := m.Streams[0]; v.Width != 426 || v.Height != 240 {
					t.Errorf("%dx%d, want 426x240", v.Width, v.Height)
				}
			}},
		{id: "gif", set: preset.Values{"width": "320", "length": "1"}, streams: []stream{{"video", "gif"}},
			check: func(t *testing.T, m *ffmpeg.Media, _ preset.Values) {
				if v := m.Streams[0]; v.Width != 320 || v.Height != 180 {
					t.Errorf("%dx%d, want 320x180", v.Width, v.Height)
				}
			}},
		{id: "streams", set: preset.Values{"stream1": "drop"}, streams: []stream{{"video", "mpeg4"}, {"subtitle", "subrip"}}},
	}

	for i, tt := range tests {
		t.Run(tt.id+tt.set["mode"], func(t *testing.T) {
			p := byID(t, tt.id)
			work := filepath.Join(dir, "work", tt.id)
			if err := os.MkdirAll(filepath.Dir(work), 0o755); err != nil {
				t.Fatal(err)
			}
			c := preset.Context{Input: input, Media: media, Caps: tools.Caps, Work: work}
			if err := p.Check(c); err != nil {
				t.Fatal(err)
			}
			v, _ := preset.Fill(p, c, tt.set)
			out := preset.DefaultOutput(p, c, v, filepath.Join(dir, "out", string(rune('a'+i))))
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				t.Fatal(err)
			}
			plan, err := p.Build(c, v, out)
			if err != nil {
				t.Fatal(err)
			}

			q := job.NewQueue(1, job.StartFFmpeg)
			q.Add(job.Spec{Name: out, Program: tools.FFmpeg, Steps: plan.Steps, Duration: plan.Duration, Output: out, Temp: plan.Temp})
			for q.Active() > 0 {
				select {
				case <-q.Changed():
				case <-time.After(2 * time.Minute):
					t.Fatal("the job did not finish")
				}
			}
			if j := q.Jobs()[0]; j.State != job.Done {
				t.Fatalf("%s: %v\n%q", j.State, j.Err, j.Log)
			}

			m, err := tools.Probe(ctx, out)
			if err != nil {
				t.Fatal(err)
			}
			var got []stream
			for _, s := range m.Streams {
				got = append(got, stream{s.Type, s.Codec})
			}
			if len(got) != len(tt.streams) {
				t.Fatalf("streams = %v, want %v", got, tt.streams)
			}
			for i := range got {
				if got[i] != tt.streams[i] {
					t.Errorf("streams = %v, want %v", got, tt.streams)
				}
			}
			if tt.check != nil {
				tt.check(t, m, v)
			}
			if left, _ := filepath.Glob(work + "*"); len(left) > 0 {
				t.Errorf("temporary files left: %q", left)
			}
		})
	}
}
