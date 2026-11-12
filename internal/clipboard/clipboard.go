// Package clipboard copies text with the operating system's own clipboard
// tool. The UI also sends the text to the terminal with OSC 52, which works
// over SSH but not in every terminal; this covers terminals without it, such
// as macOS Terminal.app.
package clipboard

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

// ErrNoTool means this system has no clipboard tool fftui can use, such as
// over SSH or on a Linux console.
var ErrNoTool = errors.New("no clipboard tool found")

type tool struct {
	name string
	args []string
}

// tools lists the clipboard programs to try on goos, in order.
func tools(goos string, getenv func(string) string) []tool {
	switch goos {
	case "darwin":
		return []tool{{"pbcopy", nil}}
	case "windows":
		return []tool{{"clip.exe", nil}}
	}
	var ts []tool
	if getenv("WAYLAND_DISPLAY") != "" {
		ts = append(ts, tool{"wl-copy", nil})
	}
	if getenv("DISPLAY") != "" {
		ts = append(ts, tool{"xclip", []string{"-selection", "clipboard"}}, tool{"xsel", []string{"--clipboard", "--input"}})
	}
	return ts
}

// Copy puts text on the clipboard with the first tool that is installed.
func Copy(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, t := range tools(runtime.GOOS, os.Getenv) {
		path, err := exec.LookPath(t.name)
		if err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, path, t.args...)
		cmd.Stdin = strings.NewReader(encode(runtime.GOOS, text))
		return cmd.Run()
	}
	return ErrNoTool
}

// encode returns text as the tool reads it. clip.exe reads the console's
// code page unless the text is UTF-16 with a byte order mark.
func encode(goos, text string) string {
	if goos != "windows" {
		return text
	}
	units := utf16.Encode([]rune("\ufeff" + text))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		b[2*i], b[2*i+1] = byte(u), byte(u>>8)
	}
	return string(b)
}
