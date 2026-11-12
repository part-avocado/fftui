package ffmpeg_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

// fixtureRunner answers `ffmpeg -hide_banner -<flag>` with
// testdata/<dir>/<flag>.txt, which was captured from that FFmpeg version.
func fixtureRunner(t *testing.T, dir string, transform func(string) string) ffmpeg.Runner {
	t.Helper()
	return ffmpeg.RunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/opt/ff/ffmpeg" || len(args) != 2 || args[0] != "-hide_banner" {
			t.Fatalf("unexpected command %s %q", name, args)
		}
		data, err := os.ReadFile(filepath.Join("testdata", dir, strings.TrimPrefix(args[1], "-")+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if transform != nil {
			return []byte(transform(string(data))), nil
		}
		return data, nil
	})
}

func names(codecs []ffmpeg.Codec) []string {
	var out []string
	for _, c := range codecs {
		out = append(out, c.Name)
	}
	return out
}

// The expected values were counted from the fixture files with awk and grep,
// independently of the parser.
var detectCases = []struct {
	dir                  string
	version              string
	major, minor         int
	encoders, decoders   int
	muxers               int
	hwEncoders, hwAccels []string
	moreMuxers           []string
}{
	{
		dir: "ffmpeg-4.4", version: "4.4.1-static", major: 4, minor: 4,
		encoders: 187, decoders: 487, muxers: 174,
		hwEncoders: []string{"h263_v4l2m2m", "h264_v4l2m2m", "hevc_v4l2m2m", "mpeg4_v4l2m2m", "vp8_v4l2m2m"},
		hwAccels:   []string{"vdpau"},
	},
	{
		dir: "ffmpeg-5.1", version: "5.1.9-0+deb12u1", major: 5, minor: 1,
		encoders: 217, decoders: 516, muxers: 186,
		hwEncoders: []string{
			"h263_v4l2m2m", "h264_nvenc", "h264_omx", "h264_qsv", "h264_v4l2m2m", "h264_vaapi",
			"hevc_nvenc", "hevc_qsv", "hevc_v4l2m2m", "hevc_vaapi", "mjpeg_qsv", "mjpeg_vaapi",
			"mpeg2_qsv", "mpeg2_vaapi", "mpeg4_omx", "mpeg4_v4l2m2m", "vp8_v4l2m2m", "vp8_vaapi",
			"vp9_vaapi", "vp9_qsv",
		},
		hwAccels:   []string{"vdpau", "cuda", "vaapi", "qsv", "drm", "opencl", "vulkan"},
		moreMuxers: []string{"sdl", "sdl2"},
	},
	{
		dir: "ffmpeg-9.0", version: "n9.0.2-48-gd4070a14ce-20261110", major: 9, minor: 0,
		encoders: 229, decoders: 556, muxers: 191,
		hwEncoders: []string{
			"av1_nvenc", "av1_qsv", "av1_amf", "av1_vaapi", "av1_vulkan", "ffv1_vulkan",
			"h263_v4l2m2m", "h264_amf", "h264_nvenc", "h264_qsv", "h264_v4l2m2m", "h264_vaapi",
			"h264_vulkan", "hevc_amf", "hevc_nvenc", "hevc_qsv", "hevc_v4l2m2m", "hevc_vaapi",
			"hevc_vulkan", "mjpeg_qsv", "mjpeg_vaapi", "mpeg2_qsv", "mpeg2_vaapi", "mpeg4_v4l2m2m",
			"prores_ks_vulkan", "vp8_v4l2m2m", "vp8_vaapi", "vp9_vaapi", "vp9_qsv",
		},
		hwAccels: []string{"cuda", "vaapi", "qsv", "drm", "opencl", "vulkan", "amf"},
	},
}

func TestDetect(t *testing.T) {
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	for _, tc := range detectCases {
		for _, lineEnd := range []string{"LF", "CRLF"} {
			t.Run(tc.dir+"/"+lineEnd, func(t *testing.T) {
				var transform func(string) string
				if lineEnd == "CRLF" {
					transform = crlf // as Windows builds may write
				}
				c, err := ffmpeg.Detect(context.Background(), fixtureRunner(t, tc.dir, transform), "/opt/ff/ffmpeg")
				if err != nil {
					t.Fatal(err)
				}
				want := ffmpeg.Version{String: tc.version, Major: tc.major, Minor: tc.minor, Known: true}
				if c.Version != want {
					t.Errorf("version = %+v, want %+v", c.Version, want)
				}
				if len(c.Encoders) != tc.encoders || len(c.Decoders) != tc.decoders || len(c.Muxers) != tc.muxers {
					t.Errorf("got %d encoders, %d decoders, %d muxers; want %d, %d, %d",
						len(c.Encoders), len(c.Decoders), len(c.Muxers), tc.encoders, tc.decoders, tc.muxers)
				}
				if got := names(c.HardwareEncoders()); !slices.Equal(got, tc.hwEncoders) {
					t.Errorf("hardware encoders = %q\nwant %q", got, tc.hwEncoders)
				}
				if !slices.Equal(c.HWAccels, tc.hwAccels) {
					t.Errorf("hwaccels = %q, want %q", c.HWAccels, tc.hwAccels)
				}

				wantCodecs := []struct {
					list []ffmpeg.Codec
					want ffmpeg.Codec
				}{
					{c.Encoders, ffmpeg.Codec{Name: "libx264", Kind: 'V', Codec: "h264", Description: "libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10"}},
					{c.Encoders, ffmpeg.Codec{Name: "aac", Kind: 'A', Codec: "aac", Description: "AAC (Advanced Audio Coding)"}},
					{c.Encoders, ffmpeg.Codec{Name: "srt", Kind: 'S', Codec: "subrip", Description: "SubRip subtitle"}},
					{c.Decoders, ffmpeg.Codec{Name: "hevc", Kind: 'V', Codec: "hevc", Description: "HEVC (High Efficiency Video Coding)"}},
				}
				for _, wc := range wantCodecs {
					i := slices.IndexFunc(wc.list, func(c ffmpeg.Codec) bool { return c.Name == wc.want.Name })
					if i < 0 {
						t.Errorf("%s missing", wc.want.Name)
					} else if wc.list[i] != wc.want {
						t.Errorf("got %+v, want %+v", wc.list[i], wc.want)
					}
				}

				for _, m := range append([]string{"mp4", "matroska", "webm", "stream_segment", "ssegment", "v4l2"}, tc.moreMuxers...) {
					if !slices.Contains(c.Muxers, m) {
						t.Errorf("muxer %q missing", m)
					}
				}
				for _, m := range c.Muxers {
					if m == "" || strings.ContainsAny(m, ", \r") {
						t.Errorf("bad muxer name %q", m)
					}
				}
			})
		}
	}
}

func TestDetectReportsFailure(t *testing.T) {
	broken := errors.New("exit status 1")
	r := ffmpeg.RunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[1] == "-encoders" {
			return nil, broken
		}
		return os.ReadFile(filepath.Join("testdata", "ffmpeg-5.1", strings.TrimPrefix(args[1], "-")+".txt"))
	})
	if _, err := ffmpeg.Detect(context.Background(), r, "ffmpeg"); !errors.Is(err, broken) {
		t.Errorf("err = %v, want %v", err, broken)
	}
}

func TestVersion(t *testing.T) {
	tests := []struct {
		line string
		want ffmpeg.Version
	}{
		{"ffmpeg version 7.1.1 Copyright (c) 2000-2025 the FFmpeg developers", ffmpeg.Version{String: "7.1.1", Major: 7, Minor: 1, Known: true}},
		{"ffmpeg version 6.1.1-full_build-www.gyan.dev Copyright (c) 2000-2023", ffmpeg.Version{String: "6.1.1-full_build-www.gyan.dev", Major: 6, Minor: 1, Known: true}},
		{"ffmpeg version 10.12 Copyright", ffmpeg.Version{String: "10.12", Major: 10, Minor: 12, Known: true}},
		{"ffmpeg version N-117045-g1a2b3c4d5e-20240901 Copyright", ffmpeg.Version{String: "N-117045-g1a2b3c4d5e-20240901"}},
		{"ffmpeg version 2026-11-01-git-abc123-full_build-www.gyan.dev Copyright", ffmpeg.Version{String: "2026-11-01-git-abc123-full_build-www.gyan.dev"}},
	}
	for _, tt := range tests {
		r := ffmpeg.RunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[1] == "-version" {
				return []byte(tt.line + "\r\nbuilt with gcc\n"), nil
			}
			return nil, nil
		})
		c, err := ffmpeg.Detect(context.Background(), r, "ffmpeg")
		if err != nil {
			t.Fatalf("%q: %v", tt.line, err)
		}
		if c.Version != tt.want {
			t.Errorf("%q: got %+v, want %+v", tt.line, c.Version, tt.want)
		}
	}

	r := ffmpeg.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("usage: something else\n"), nil
	})
	if _, err := ffmpeg.Detect(context.Background(), r, "notffmpeg"); err == nil {
		t.Error("a program that is not ffmpeg was accepted")
	}
}

func TestVersionAtLeast(t *testing.T) {
	v := func(major, minor int) ffmpeg.Version { return ffmpeg.Version{Major: major, Minor: minor, Known: true} }
	tests := []struct {
		v            ffmpeg.Version
		major, minor int
		want         bool
	}{
		{v(4, 4), 4, 4, true},
		{v(4, 3), 4, 4, false},
		{v(5, 0), 4, 4, true}, // newer major, lower minor
		{v(3, 9), 4, 4, false},
		{v(4, 4), 5, 0, false},
		{ffmpeg.Version{}, 99, 0, true}, // git builds count as new
	}
	for _, tt := range tests {
		if got := tt.v.AtLeast(tt.major, tt.minor); got != tt.want {
			t.Errorf("%d.%d AtLeast(%d, %d) = %t, want %t", tt.v.Major, tt.v.Minor, tt.major, tt.minor, got, tt.want)
		}
	}
}

func TestHardwareAPI(t *testing.T) {
	tests := map[string]string{
		"h264_nvenc":        "NVIDIA NVENC",
		"hevc_videotoolbox": "Apple VideoToolbox",
		"av1_qsv":           "Intel Quick Sync",
		"h264_mf":           "Media Foundation",
		"prores_ks_vulkan":  "Vulkan",
		"libx264":           "",
		"pcm_s16le":         "",
		"adpcm_ima_qt":      "",
		"libsvtav1":         "",
	}
	for name, want := range tests {
		if got := (ffmpeg.Codec{Name: name}).HardwareAPI(); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}
