package clipboard

import (
	"slices"
	"testing"
)

func TestTools(t *testing.T) {
	names := func(ts []tool) (out []string) {
		for _, t := range ts {
			out = append(out, t.name)
		}
		return out
	}
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	for _, c := range []struct {
		goos string
		env  map[string]string
		want []string
	}{
		{"darwin", nil, []string{"pbcopy"}},
		{"windows", nil, []string{"clip.exe"}},
		{"linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}, []string{"wl-copy", "xclip", "xsel"}},
		{"freebsd", map[string]string{"DISPLAY": ":0"}, []string{"xclip", "xsel"}},
		// No display, as over SSH: the terminal's OSC 52 is the only way.
		{"linux", nil, nil},
	} {
		if got := names(tools(c.goos, env(c.env))); !slices.Equal(got, c.want) {
			t.Errorf("%s %v: tools = %q, want %q", c.goos, c.env, got, c.want)
		}
	}
}

func TestEncodeForClipExe(t *testing.T) {
	// A UTF-16LE byte order mark, then "é" (U+00E9) and "日" (U+65E5).
	if got, want := encode("windows", "é日"), "\xff\xfe\xe9\x00\xe5\x65"; got != want {
		t.Errorf("encode = %q, want %q", got, want)
	}
	if got := encode("linux", "é日"); got != "é日" {
		t.Errorf("encode changed text for linux: %q", got)
	}
}
