package preset_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/preset"
)

// fixture returns a Context for one of the files real ffprobe described in
// ../ffmpeg/testdata/ffmpeg-<version>, with that build's encoders.
func fixture(t *testing.T, version, probe, input string) preset.Context {
	t.Helper()
	dir := filepath.Join("..", "ffmpeg", "testdata", "ffmpeg-"+version)
	r := ffmpeg.RunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			return os.ReadFile(filepath.Join(dir, probe))
		}
		return os.ReadFile(filepath.Join(dir, strings.TrimPrefix(args[1], "-")+".txt"))
	})
	caps, err := ffmpeg.Detect(context.Background(), r, "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	tools := &ffmpeg.Tools{Binaries: ffmpeg.Binaries{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}, Caps: caps, Runner: r}
	m, err := tools.Probe(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return preset.Context{Input: input, Media: m, Caps: caps, Work: "/tmp/fftui-work/job1"}
}

// The inputs, as ffprobe saw them: multi is 3.026s of 1280x720 H.264 with
// 5.1 Opus (index 1), stereo AAC (2), and SubRip subtitles (3). sample is 2s
// of 320x240 H.264 with AAC. song is an MP3 with cover art.
const (
	multiIn  = "/videos/My clip ü.mkv"
	sampleIn = "/videos/sample.mp4"
	songIn   = "/music/song.mp3"
	out      = "/out/new file.out"
)

func multi(t *testing.T) preset.Context  { return fixture(t, "9.0", "probe-multi.json", multiIn) }
func sample(t *testing.T) preset.Context { return fixture(t, "9.0", "probe-sample.json", sampleIn) }
func song(t *testing.T) preset.Context   { return fixture(t, "9.0", "probe-song.json", songIn) }

func byID(t *testing.T, id string) preset.Preset {
	t.Helper()
	for _, p := range preset.All() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no preset %q", id)
	return preset.Preset{}
}

// build fills the form with set on top of the defaults and builds it for
// out, or for output when it is given.
func build(t *testing.T, id string, c preset.Context, set preset.Values, output ...string) (preset.Plan, error) {
	t.Helper()
	p := byID(t, id)
	if err := p.Check(c); err != nil {
		t.Fatalf("%s rejected the input: %v", id, err)
	}
	v, _ := preset.Fill(p, c, nil)
	for k, x := range set {
		v[k] = x
	}
	v, _ = preset.Fill(p, c, v)
	for k, x := range set {
		if v[k] != x {
			t.Fatalf("%s: %s = %q is not offered; the form reset it to %q", id, k, x, v[k])
		}
	}
	if len(output) == 0 {
		output = []string{out}
	}
	return p.Build(c, v, output[0])
}

func args(s string) []string { return strings.Fields(s) }

func TestPlans(t *testing.T) {
	// Arguments with spaces are written with \x00 for the space and fixed
	// up below, so the table stays readable.
	in := func(path string) string { return strings.ReplaceAll(path, " ", "\x00") }
	o := in(out)
	tests := []struct {
		name  string
		id    string
		ctx   func(*testing.T) preset.Context
		set   preset.Values
		steps []string
		dur   time.Duration
		notes int
		// output replaces out; Trim picks its format from the extension.
		output string
	}{
		{
			name: "convert defaults: Opus cannot go in MP4 and SubRip becomes mov_text",
			id:   "convert", ctx: multi,
			steps: []string{"-i " + in(multiIn) + " -map 0:0 -map 0:1 -map 0:2 -map 0:3 -c:v libx264 -crf 23 -preset medium -pix_fmt yuv420p -c:a aac -b:a 192k -c:s mov_text -movflags +faststart " + o},
			dur:   3026 * time.Millisecond, notes: 1,
		},
		{
			name: "convert to H.265 with NVENC, high quality, fast",
			id:   "convert", ctx: multi,
			set:   preset.Values{"codec": "hevc", "encoder": "hevc_nvenc", "quality": "high", "speed": "fast", "audio": "none", "subtitles": "drop"},
			steps: []string{"-i " + in(multiIn) + " -map 0:0 -c:v hevc_nvenc -rc vbr -cq 19 -b:v 0 -preset p2 -tag:v hvc1 -movflags +faststart " + o},
			dur:   3026 * time.Millisecond,
		},
		{
			name: "convert with VA-API uploads frames to the device",
			id:   "convert", ctx: sample,
			set:   preset.Values{"encoder": "h264_vaapi"},
			steps: []string{"-init_hw_device vaapi=hw -filter_hw_device hw -i /videos/sample.mp4 -map 0:0 -map 0:1 -c:v h264_vaapi -qp 25 -vf format=nv12,hwupload -c:a copy -movflags +faststart " + o},
			dur:   2 * time.Second,
		},
		{
			name: "convert to WebM keeps Opus and turns SubRip into WebVTT",
			id:   "convert", ctx: multi,
			set:   preset.Values{"container": "webm", "audio": "copy"},
			steps: []string{"-i " + in(multiIn) + " -map 0:0 -map 0:1 -map 0:2 -map 0:3 -c:v libsvtav1 -crf 35 -preset 8 -c:a libopus -b:a 128k -c:s webvtt " + o},
			dur:   3026 * time.Millisecond, notes: 1, // AAC cannot go in WebM
		},
		{
			name: "change container copies the video",
			id:   "remux", ctx: sample,
			steps: []string{"-i /videos/sample.mp4 -map 0:0 -map 0:1 -c:v copy -c:a copy " + o},
			dur:   2 * time.Second,
		},
		{
			name: "two-pass compress: (1 MB × 8 × 0.97 / 3.026s) − 2 × 128 kb/s = 2308441 b/s of video",
			id:   "compress", ctx: multi,
			set: preset.Values{"size": "1"},
			steps: []string{
				"-i " + in(multiIn) + " -map 0:0 -c:v libx264 -b:v 2308441 -maxrate 2308441 -bufsize 4616882 -preset medium -pix_fmt yuv420p -fps_mode passthrough -pass 1 -passlogfile /tmp/fftui-work/job1-pass -an -sn -f null -",
				"-i " + in(multiIn) + " -map 0:0 -map 0:1 -map 0:2 -c:v libx264 -b:v 2308441 -maxrate 2308441 -bufsize 4616882 -preset medium -pix_fmt yuv420p -fps_mode passthrough -pass 2 -passlogfile /tmp/fftui-work/job1-pass -c:a aac -b:a 128k -movflags +faststart " + o,
			},
			dur: 3026 * time.Millisecond,
		},
		{
			name: "one-pass compress without audio: 1 MB × 8 × 0.97 / 2s",
			id:   "compress", ctx: sample,
			set:   preset.Values{"size": "1", "passes": "1", "audio": "none"},
			steps: []string{"-i /videos/sample.mp4 -map 0:0 -c:v libx264 -b:v 3880000 -maxrate 3880000 -bufsize 7760000 -preset medium -pix_fmt yuv420p -movflags +faststart " + o},
			dur:   2 * time.Second, notes: 1, // the file is already smaller
		},
		{
			name: "extract audio defaults to the first track as MP3",
			id:   "audio", ctx: multi,
			steps: []string{"-i " + in(multiIn) + " -map 0:1 -c:a libmp3lame -q:a 2 " + o},
			dur:   3026 * time.Millisecond,
		},
		{
			name: "extract the second track as low-quality Opus",
			id:   "audio", ctx: multi,
			set:   preset.Values{"track": "2", "format": "opus", "quality": "low"},
			steps: []string{"-i " + in(multiIn) + " -map 0:2 -c:a libopus -b:a 96k " + o},
			dur:   3026 * time.Millisecond,
		},
		{
			name: "extract audio without converting it",
			id:   "audio", ctx: song,
			set:   preset.Values{"format": "copy"},
			steps: []string{"-i /music/song.mp3 -map 0:0 -c:a copy " + o},
			dur:   song(t).Media.Duration,
		},
		{
			name: "fast trim",
			id:   "trim", ctx: sample,
			set:   preset.Values{"start": "0.5", "end": "0:00:01.75"},
			steps: []string{"-ss 0.5 -i /videos/sample.mp4 -map 0:v? -map 0:a? -map 0:s? -c copy -avoid_negative_ts make_zero -t 1.25 " + o},
			dur:   1250 * time.Millisecond, notes: 1,
		},
		{
			name: "exact trim re-encodes",
			id:   "trim", ctx: sample,
			set:    preset.Values{"start": "1", "end": "2", "mode": "exact"},
			steps:  []string{"-ss 1 -i /videos/sample.mp4 -map 0:0 -map 0:1 -c:v libx264 -crf 18 -preset medium -pix_fmt yuv420p -c:a aac -b:a 192k -movflags +faststart -t 1 /out/cut.mp4"},
			dur:    time.Second,
			output: "/out/cut.mp4",
		},
		{
			name: "exact trim of an MP3 re-encodes only the audio",
			id:   "trim", ctx: song,
			set:    preset.Values{"start": "0", "end": "1", "mode": "exact"},
			steps:  []string{"-ss 0 -i /music/song.mp3 -map 0:a -c:a libmp3lame -b:a 192k -t 1 /out/cut.mp3"},
			dur:    time.Second,
			output: "/out/cut.mp3",
		},
		{
			name: "resize to 480p at 24 fps",
			id:   "resize", ctx: multi,
			set:   preset.Values{"height": "480", "fps": "24", "audio": "aac"},
			steps: []string{"-i " + in(multiIn) + " -map 0:0 -map 0:1 -map 0:2 -map 0:3 -c:v libx264 -crf 23 -preset medium -vf scale=-2:480,fps=24 -pix_fmt yuv420p -c:a aac -b:a 192k -c:s mov_text -movflags +faststart " + o},
			dur:   3026 * time.Millisecond,
		},
		{
			name: "GIF",
			id:   "gif", ctx: multi,
			set:   preset.Values{"start": "1", "length": "1.5", "width": "320", "fps": "10"},
			steps: []string{"-ss 1 -t 1.5 -i " + in(multiIn) + " -map 0:0 -vf fps=10,scale=320:-1:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse -loop 0 " + o},
			dur:   1500 * time.Millisecond,
		},
		{
			name: "GIF past the end is cut to the end",
			id:   "gif", ctx: sample,
			set:   preset.Values{"start": "1.5", "length": "5"},
			steps: []string{"-ss 1.5 -t 0.5 -i /videos/sample.mp4 -map 0:0 -vf fps=15,scale=320:-1:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse -loop 0 " + o},
			dur:   500 * time.Millisecond,
		},
		{
			name: "choose streams drops the Opus track",
			id:   "streams", ctx: multi,
			set:   preset.Values{"stream1": "drop"},
			steps: []string{"-i " + in(multiIn) + " -map 0:0 -map 0:2 -map 0:3 -c copy " + o},
			dur:   3026 * time.Millisecond,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := out
			if tt.output != "" {
				output = tt.output
			}
			plan, err := build(t, tt.id, tt.ctx(t), tt.set, output)
			if err != nil {
				t.Fatal(err)
			}
			var want [][]string
			for _, s := range tt.steps {
				a := args(s)
				for i := range a {
					a[i] = strings.ReplaceAll(a[i], "\x00", " ")
				}
				want = append(want, a)
			}
			if !slices.EqualFunc(plan.Steps, want, slices.Equal) {
				for i := range max(len(plan.Steps), len(want)) {
					var g, w []string
					if i < len(plan.Steps) {
						g = plan.Steps[i]
					}
					if i < len(want) {
						w = want[i]
					}
					t.Errorf("step %d:\n got  %q\n want %q", i+1, g, w)
				}
			}
			if plan.Duration != tt.dur {
				t.Errorf("duration = %v, want %v", plan.Duration, tt.dur)
			}
			if len(plan.Notes) != tt.notes {
				t.Errorf("notes = %q, want %d", plan.Notes, tt.notes)
			}
		})
	}
}

func TestPlanErrors(t *testing.T) {
	tests := []struct {
		id   string
		ctx  func(*testing.T) preset.Context
		set  preset.Values
		want string
	}{
		{"trim", sample, preset.Values{"start": "1.5", "end": "1"}, "the end must be after the start"},
		{"trim", sample, preset.Values{"end": "0:00:03"}, "after the end of the file, 0:00:02.000"},
		{"trim", sample, preset.Values{"start": "1:75"}, `start: "1:75" is not a time`},
		{"gif", sample, preset.Values{"start": "2"}, "the start is after the end"},
		{"gif", sample, preset.Values{"length": "0"}, "the length must be"},
		{"compress", sample, preset.Values{"size": "big"}, "the target size must be a number"},
		// 2s of 100 kb/s video and 128 kb/s audio is 57 kB; with the 3%
		// margin it rounds up to 0.1 MB.
		{"compress", sample, preset.Values{"size": "0.01"}, "0.01 MB is too small for 0:00:02.000 of video; it needs at least 0.1 MB"},
		{"streams", sample, preset.Values{"stream0": "drop", "stream1": "drop"}, "keep at least one stream"},
		// .out is not a format Trim knows how to re-encode.
		{"trim", song, preset.Values{"start": "0", "end": "1", "mode": "exact"}, "exact cuts are not supported for this format"},
	}
	for _, tt := range tests {
		_, err := build(t, tt.id, tt.ctx(t), tt.set)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %v: err = %v, want %q", tt.id, tt.set, err, tt.want)
		}
	}
}

func TestChecks(t *testing.T) {
	tests := []struct {
		id   string
		ctx  func(*testing.T) preset.Context
		want string // "" means it accepts the input
	}{
		{"convert", song, "the file has no video"}, // cover art is not video
		{"resize", sample, "only 240 pixels tall"},
		{"resize", multi, ""},
		{"audio", song, ""},
		{"trim", song, ""},
		{"streams", sample, ""},
	}
	for _, tt := range tests {
		err := byID(t, tt.id).Check(tt.ctx(t))
		if (err == nil) != (tt.want == "") || err != nil && !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Check = %v, want %q", tt.id, err, tt.want)
		}
	}
}

func optionValues(f preset.Field) []string {
	var vals []string
	for _, o := range f.Options {
		vals = append(vals, o.Value)
	}
	return vals
}

func field(t *testing.T, fields []preset.Field, key string) preset.Field {
	t.Helper()
	for _, f := range fields {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("no %q field in %+v", key, fields)
	return preset.Field{}
}

func TestEncoderChoice(t *testing.T) {
	c := multi(t)
	p := byID(t, "convert")
	v, fields := preset.Fill(p, c, nil)
	// The software encoder comes first and is the default, then every
	// hardware encoder FFmpeg 9.0 has for H.264.
	enc := field(t, fields, preset.EncoderKey)
	want := []string{"libx264", "h264_amf", "h264_nvenc", "h264_qsv", "h264_v4l2m2m", "h264_vaapi", "h264_vulkan"}
	if !slices.Equal(optionValues(enc), want) || v[preset.EncoderKey] != "libx264" {
		t.Errorf("encoders = %q, chosen %q", optionValues(enc), v[preset.EncoderKey])
	}

	// Choosing another codec moves the encoder to that codec's software one.
	v["codec"] = "av1"
	v, fields = preset.Fill(p, c, v)
	if v[preset.EncoderKey] != "libsvtav1" || !slices.Contains(optionValues(field(t, fields, preset.EncoderKey)), "av1_nvenc") {
		t.Errorf("after choosing AV1: encoder %q from %q", v[preset.EncoderKey], optionValues(field(t, fields, preset.EncoderKey)))
	}
	// WebM cannot hold H.264, so the codec moves too.
	v["codec"], v["container"] = "h264", "webm"
	v, _ = preset.Fill(p, c, v)
	if v["codec"] != "av1" {
		t.Errorf("WebM codec = %q, want av1, its first codec", v["codec"])
	}
	// V4L2 has no speed setting, so the Speed field goes away.
	v["container"], v["codec"], v[preset.EncoderKey] = "mp4", "h264", "h264_v4l2m2m"
	_, fields = preset.Fill(p, c, v)
	if slices.ContainsFunc(fields, func(f preset.Field) bool { return f.Key == "speed" }) {
		t.Error("speed offered for h264_v4l2m2m")
	}
	if !preset.IsHardware("h264_v4l2m2m") || preset.IsHardware("libx264") {
		t.Error("IsHardware is wrong")
	}
}

func TestOlderFFmpeg(t *testing.T) {
	// FFmpeg 4.4 has -vsync instead of -fps_mode.
	plan, err := build(t, "compress", fixture(t, "4.4", "probe-sample.json", sampleIn), preset.Values{"size": "1"})
	if err != nil {
		t.Fatal(err)
	}
	for i, step := range plan.Steps {
		if j := slices.Index(step, "-vsync"); j < 0 || step[j+1] != "passthrough" || slices.Contains(step, "-fps_mode") {
			t.Errorf("4.4 pass %d: %q", i+1, step)
		}
	}

	// FFmpeg 4.4 has no SVT-AV1 here; AV1 falls back to libaom.
	c := fixture(t, "4.4", "probe-multi.json", multiIn)
	plan, err = build(t, "convert", c, preset.Values{"container": "mkv", "codec": "av1", "audio": "copy", "subtitles": "keep"})
	if err != nil {
		t.Fatal(err)
	}
	want := args("-i x -map 0:0 -map 0:1 -map 0:2 -map 0:3 -c:v libaom-av1 -crf 32 -b:v 0 -cpu-used 6 -row-mt 1 -c:a copy -c:s copy")
	want[1] = multiIn
	if got := plan.Steps[0]; !slices.Equal(got[:len(got)-1], want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestDefaultOutput(t *testing.T) {
	c := multi(t)
	tests := []struct {
		id, dir, want string
	}{
		{"convert", "", "/videos/My clip ü-converted.mp4"},
		{"remux", "", "/videos/My clip ü-remux.mp4"}, // MKV in, so MP4 is the default
		{"audio", "/srv/out", "/srv/out/My clip ü-audio.mp3"},
		{"trim", "", "/videos/My clip ü-trim.mkv"},
		{"gif", "", "/videos/My clip ü.gif"},
	}
	for _, tt := range tests {
		p := byID(t, tt.id)
		v, _ := preset.Fill(p, c, nil)
		if got := preset.DefaultOutput(p, c, v, filepath.FromSlash(tt.dir)); got != filepath.FromSlash(tt.want) {
			t.Errorf("%s: got %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestParseTime(t *testing.T) {
	ok := map[string]time.Duration{
		"90":           90 * time.Second,
		"1.5":          1500 * time.Millisecond,
		"1:30":         90 * time.Second,
		"1:30.25":      90250 * time.Millisecond,
		"0:01:30":      90 * time.Second,
		" 2:00:00.5 ":  2*time.Hour + 500*time.Millisecond,
		"0:00:00.000":  0,
		"0:00:03.026":  3026 * time.Millisecond,
		"100:00":       100 * time.Minute, // the first part can be any size
		"0.0001":       0,
		"59.9999":      60 * time.Second,
		"1:59:59.999":  2*time.Hour - time.Millisecond,
		"10:00:00.123": 10*time.Hour + 123*time.Millisecond,
	}
	for s, want := range ok {
		if got, err := preset.ParseTime(s); err != nil || got != want {
			t.Errorf("ParseTime(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "abc", "1:60", "1:2:3:4", "-5", "1.5:30", "1::30", "1:-30", "1:30x"} {
		if _, err := preset.ParseTime(s); err == nil {
			t.Errorf("ParseTime(%q) did not fail", s)
		}
	}
}

func TestEdgeCases(t *testing.T) {
	// H.265 in MKV gets no hvc1 tag; that is only for Apple's MP4 and MOV.
	plan, err := build(t, "convert", multi(t), preset.Values{"container": "mkv", "codec": "hevc", "audio": "copy"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Steps[0], "-tag:v") {
		t.Errorf("hvc1 tag in MKV: %q", plan.Steps[0])
	}

	// Picture subtitles, such as Blu-ray's, cannot go in MP4.
	c := multi(t)
	c.Media.Streams[3].Codec = "hdmv_pgs_subtitle"
	plan, err = build(t, "convert", c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Steps[0], "0:3") || slices.Contains(plan.Steps[0], "-c:s") ||
		!slices.ContainsFunc(plan.Notes, func(n string) bool { return strings.Contains(n, "picture-based") }) {
		t.Errorf("picture subtitles: %q, notes %q", plan.Steps[0], plan.Notes)
	}
	if _, err := build(t, "streams", c, nil, "/out/x.mp4"); err == nil || !strings.Contains(err.Error(), "picture-based") {
		t.Errorf("choose streams to MP4 with picture subtitles: err = %v", err)
	}

	// FFmpeg 5.0 has SVT-AV1 without -crf, and with presets up to 8 only.
	c = multi(t)
	c.Caps.Version = ffmpeg.Version{String: "5.0.1", Major: 5, Minor: 0, Known: true}
	plan, err = build(t, "convert", c, preset.Values{"container": "mkv", "codec": "av1", "audio": "copy"})
	if err != nil {
		t.Fatal(err)
	}
	want := args("-c:v libsvtav1 -rc 0 -qp 35 -preset 6")
	if i := slices.Index(plan.Steps[0], "-c:v"); i < 0 || !slices.Equal(plan.Steps[0][i:i+len(want)], want) {
		t.Errorf("SVT-AV1 on 5.0: %q", plan.Steps[0])
	}
}
