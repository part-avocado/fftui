package ffmpeg_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

// probeTools answers ffprobe with testdata/<dir>/<file>, recording the
// arguments it was called with.
func probeTools(t *testing.T, dir, file string, gotArgs *[]string) *ffmpeg.Tools {
	t.Helper()
	return &ffmpeg.Tools{
		Binaries: ffmpeg.Binaries{FFmpeg: "ffmpeg", FFprobe: "/opt/ff/ffprobe"},
		Runner: ffmpeg.RunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "/opt/ff/ffprobe" {
				t.Fatalf("ran %s, want ffprobe", name)
			}
			if gotArgs != nil {
				*gotArgs = args
			}
			return os.ReadFile(filepath.Join("testdata", dir, file))
		}),
	}
}

var fixtureDirs = []string{"ffmpeg-4.4", "ffmpeg-5.1", "ffmpeg-9.0"}

func TestProbeMultiStream(t *testing.T) {
	// The file was made with `ffmpeg ... -metadata title="Big Buck Test"`, an
	// H.264 10-bit video, 5.1 Opus (eng), stereo AAC (jpn, titled
	// Commentary), and forced English SubRip subtitles.
	want := []ffmpeg.Stream{
		{Index: 0, Type: "video", Codec: "h264", CodecLong: "H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10", Profile: "High 10",
			Width: 1280, Height: 720, FrameRate: 30000.0 / 1001, PixelFormat: "yuv420p10le"},
		{Index: 1, Type: "audio", Codec: "opus", CodecLong: "Opus (Opus Interactive Audio Codec)",
			SampleRate: 48000, Channels: 6, ChannelLayout: "5.1", Language: "eng"},
		{Index: 2, Type: "audio", Codec: "aac", CodecLong: "AAC (Advanced Audio Coding)", Profile: "LC",
			SampleRate: 44100, Channels: 2, ChannelLayout: "stereo", Language: "jpn", Title: "Commentary"},
		{Index: 3, Type: "subtitle", Codec: "subrip", CodecLong: "SubRip subtitle", Language: "eng", Forced: true},
	}
	for _, dir := range fixtureDirs {
		t.Run(dir, func(t *testing.T) {
			m, err := probeTools(t, dir, "probe-multi.json", nil).Probe(context.Background(), "multi stream.mkv")
			if err != nil {
				t.Fatal(err)
			}
			if m.Path != "multi stream.mkv" || m.Format != "matroska,webm" || m.FormatLong != "Matroska / WebM" ||
				m.Duration != 3026*time.Millisecond || m.Size != 2984611 || m.BitRate != 7890577 || m.Title != "Big Buck Test" {
				t.Errorf("format = %+v", *m)
			}
			if len(m.Streams) != len(want) {
				t.Fatalf("got %d streams, want %d", len(m.Streams), len(want))
			}
			for i, s := range m.Streams {
				if s != want[i] {
					t.Errorf("stream %d:\n got %+v\nwant %+v", i, s, want[i])
				}
			}
		})
	}
}

func TestProbeCoverArtAndDefaults(t *testing.T) {
	for _, dir := range fixtureDirs {
		t.Run(dir, func(t *testing.T) {
			song, err := probeTools(t, dir, "probe-song.json", nil).Probe(context.Background(), "song.mp3")
			if err != nil {
				t.Fatal(err)
			}
			if song.Title != "Sine Song" || len(song.Streams) != 2 {
				t.Fatalf("got %+v", *song)
			}
			audio, art := song.Streams[0], song.Streams[1]
			if audio.Codec != "mp3" || audio.BitRate != 192000 || audio.Channels != 1 || audio.ChannelLayout != "mono" {
				t.Errorf("audio = %+v", audio)
			}
			// The cover's r_frame_rate is 90000/1, which is not a frame rate.
			if !art.CoverArt || art.Type != "video" || art.Width != 300 || art.FrameRate != 0 {
				t.Errorf("cover art = %+v", art)
			}

			mp4, err := probeTools(t, dir, "probe-sample.json", nil).Probe(context.Background(), "sample.mp4")
			if err != nil {
				t.Fatal(err)
			}
			v, a := mp4.Streams[0], mp4.Streams[1]
			// "und" (undetermined) means no language.
			if v.Language != "" || a.Language != "eng" || !v.Default || !a.Default || v.FrameRate != 25 || v.CoverArt {
				t.Errorf("video %+v\naudio %+v", v, a)
			}
		})
	}
}

func TestProbeUpperCaseTags(t *testing.T) {
	// Made with ffmpeg -metadata TITLE="Upper Title"; ffprobe keeps the case.
	m, err := probeTools(t, "ffmpeg-9.0", "probe-flac-upper.json", nil).Probe(context.Background(), "upper.flac")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Upper Title" {
		t.Errorf("title = %q, want %q", m.Title, "Upper Title")
	}
}

func TestProbeArguments(t *testing.T) {
	var args []string
	path := filepath.Join("-odd dir", "clip ü \"1\".mp4")
	if _, err := probeTools(t, "ffmpeg-5.1", "probe-sample.json", &args).Probe(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	want := []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams", "-i", path}
	if !slices.Equal(args, want) {
		t.Errorf("args = %q\nwant %q", args, want)
	}
}

func TestProbeErrors(t *testing.T) {
	stderr, err := os.ReadFile(filepath.Join("testdata", "ffmpeg-9.0", "probe-notes-stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	failed := &ffmpeg.RunError{Program: "ffprobe", Err: errors.New("exit status 1"), Stderr: strings.TrimSpace(string(stderr))}
	tools := &ffmpeg.Tools{Runner: ffmpeg.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return nil, failed
	})}
	_, err = tools.Probe(context.Background(), "notes.txt")
	if err == nil || err.Error() != "ffprobe: notes.txt: Invalid data found when processing input" {
		t.Errorf("err = %v", err)
	}

	tools.Runner = ffmpeg.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("{\"streams\": [tru"), nil
	})
	if _, err := tools.Probe(context.Background(), "x.mp4"); err == nil || !strings.Contains(err.Error(), "x.mp4") {
		t.Errorf("err = %v, want a JSON error naming the file", err)
	}
}

func TestExecOutput(t *testing.T) {
	t.Setenv("FFTUI_HELPER", "probe-fail")
	out, err := ffmpeg.Exec{}.Output(context.Background(), os.Args[0], "-test.run=^TestHelperProcess$")
	var runErr *ffmpeg.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("err = %v, want a RunError", err)
	}
	if runErr.Stderr != "the real reason" || !strings.HasSuffix(err.Error(), ": the real reason") {
		t.Errorf("err = %q, stderr = %q", err, runErr.Stderr)
	}
	if string(out) != "partial output" {
		t.Errorf("stdout = %q", out)
	}

	_, err = ffmpeg.Exec{}.Output(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if !errors.As(err, &runErr) || runErr.Program != "missing" {
		t.Errorf("missing program: err = %v", err)
	}
}
