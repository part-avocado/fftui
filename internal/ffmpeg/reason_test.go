package ffmpeg_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

func TestReason(t *testing.T) {
	// Logs that real FFmpeg builds wrote when they failed.
	for file, want := range map[string]string{
		"4.4-filter.txt":  "scale filter: Invalid size 'abc'",
		"4.4-missing.txt": "nope.mp4: No such file or directory",
		"5.1-nvenc.txt":   "h264_nvenc: Cannot load libcuda.so.1",
		"5.1-badopt.txt":  "Unrecognized option 'frobnicate'.",
		"9.0-nvenc.txt":   "h264_nvenc: Cannot load libcuda.so.1",
		"9.0-missing.txt": "nope.mp4: No such file or directory",
		"9.0-noenc.txt":   "Unknown encoder 'libfoo'",
		"9.0-filter.txt":  "scale filter: Invalid size 'abc'",
		"9.0-perm.txt":    "Error opening output /proc/out.mp4: No such file or directory",
	} {
		data, err := os.ReadFile(filepath.Join("testdata", "errors", file))
		if err != nil {
			t.Fatal(err)
		}
		if got := ffmpeg.Reason(strings.Split(string(data), "\n")); got != want {
			t.Errorf("%s: Reason = %q, want %q", file, got, want)
		}
	}
}

func TestReasonFallsBackToLastLine(t *testing.T) {
	for _, c := range []struct {
		log  []string
		want string
	}{
		{nil, ""},
		{[]string{"", "  "}, ""},
		// Only generic lines: the last one is better than nothing.
		{[]string{"Input #0, mov", "Conversion failed!", ""}, "Conversion failed!"},
		// A warning long before the end is not the reason.
		{append([]string{"[mp4 @ 0x1] an early warning"}, strings.Split(strings.Repeat("frame= 1\n", 40), "\n")...), "frame= 1"},
	} {
		if got := ffmpeg.Reason(c.log); got != c.want {
			t.Errorf("Reason(%q) = %q, want %q", c.log, got, c.want)
		}
	}
}
