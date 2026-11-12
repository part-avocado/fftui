package cmdline_test

import (
	"math/rand/v2"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/part-avocado/fftui/internal/cmdline"
)

func TestJoin(t *testing.T) {
	args := []string{"-i", "my clip.mp4", "-map", "0:v?", "-vf", "scale=-2:720,fps=30", "-metadata", "title=It's \"done\"", "-c:a", "", `C:\Vidéos\out.mp4`}
	for _, c := range []struct {
		windows bool
		want    string
	}{
		{false, `ffmpeg -i 'my clip.mp4' -map '0:v?' -vf scale=-2:720,fps=30 -metadata 'title=It'"'"'s "done"' -c:a '' 'C:\Vidéos\out.mp4'`},
		{true, `ffmpeg -i "my clip.mp4" -map "0:v?" -vf "scale=-2:720,fps=30" -metadata 'title=It'"'"'s "done"' -c:a "" C:\Vidéos\out.mp4`},
	} {
		if got := cmdline.Join("ffmpeg", args, c.windows); got != c.want {
			t.Errorf("windows=%t:\n got %s\nwant %s", c.windows, got, c.want)
		}
	}
}

// Split must read back exactly what Join wrote, in both styles, whatever
// the arguments hold.
func TestSplitReadsJoin(t *testing.T) {
	runes := []rune("aZ09 \t'\"\\$`!*?[]{}(),;:=-_./~#%^&|<>éß日")
	r := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		args := make([]string, r.IntN(5))
		for i := range args {
			b := make([]rune, r.IntN(8))
			for j := range b {
				b[j] = runes[r.IntN(len(runes))]
			}
			args[i] = string(b)
		}
		for _, windows := range []bool{false, true} {
			line := cmdline.Join("ffmpeg", args, windows)
			got, err := cmdline.Split(line)
			if err != nil || !slices.Equal(got, append([]string{"ffmpeg"}, args...)) {
				t.Fatalf("windows=%t: Split(%s) = %q, %v; want %q", windows, line, got, err, args)
			}
		}
	}
}

// What Join writes for POSIX shells must reach the program unchanged.
func TestJoinWorksInRealShells(t *testing.T) {
	args := []string{"my clip.mp4", "0:v?", "*", "$HOME", "`id`", "it's", `"q"`, "", `a\b`, "!x", "a;b", "~"}
	for _, sh := range []string{"sh", "bash", "zsh"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		out, err := exec.Command(path, "-c", cmdline.Join("printf", append([]string{`%s\n`}, args...), false)).Output()
		if err != nil {
			t.Fatalf("%s: %v", sh, err)
		}
		if got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); !slices.Equal(got, args) {
			t.Errorf("%s got %q, want %q", sh, got, args)
		}
	}
}

func TestSplit(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"  -i  a.mp4\tb.mkv  ", []string{"-i", "a.mp4", "b.mkv"}},
		{`-i "C:\My Videos\a.mp4" C:\out\b.mkv`, []string{"-i", `C:\My Videos\a.mp4`, `C:\out\b.mkv`}},
		{`-metadata title="a b"'c d'`, []string{"-metadata", "title=a bc d"}},
		{`-c:a ""`, []string{"-c:a", ""}},
		{"", nil},
	} {
		got, err := cmdline.Split(c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("Split(%s) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := cmdline.Split(`-i "a.mp4`); err == nil || !strings.Contains(err.Error(), "not closed") {
		t.Errorf("unclosed quote: err = %v", err)
	}
}
