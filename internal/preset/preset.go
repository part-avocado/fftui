// Package preset defines fftui's tasks, such as converting a video or
// extracting its audio. A preset lists the form fields it needs and turns
// the user's answers into FFmpeg arguments. It does no I/O: everything it
// knows about the input comes from Context.
package preset

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

// Context is what a preset knows about its input and the FFmpeg build.
type Context struct {
	Input string // the input path, as the user gave it
	Media *ffmpeg.Media
	Caps  ffmpeg.Capabilities
	// Work is a path prefix for temporary files, such as two-pass logs.
	Work string
}

// Kind is the kind of a form field.
type Kind int

const (
	// Choice picks one of Options.
	Choice Kind = iota
	// Text is typed by the user.
	Text
)

// Option is one answer to a Choice field.
type Option struct {
	Value string
	Label string
}

// Field is one question on a preset's form.
type Field struct {
	Key     string
	Label   string
	Kind    Kind
	Options []Option // for Choice
	// Default is the value the field starts with.
	Default string
	// Hint explains a Text field's format, such as "MB".
	Hint string
}

// Values holds the answers, by field key. The output path is under the key
// "output".
type Values map[string]string

// Plan is what a preset runs.
type Plan struct {
	// Steps are the FFmpeg runs, in order, each without the "ffmpeg"
	// itself. Most presets have one; two-pass encodes have two.
	Steps [][]string
	// Duration is how long the output will be, for the progress bar. Zero
	// means unknown.
	Duration time.Duration
	// Notes explain choices the user did not make, such as a stream that
	// has to be re-encoded to fit the container.
	Notes []string
	// Temp are glob patterns of temporary files to delete afterwards.
	Temp []string
}

// Preset is one task on the menu.
type Preset struct {
	ID          string
	Name        string
	Description string
	// Suffix is added to the input's name for the default output, such as
	// "clip-small.mp4" for "small".
	Suffix string
	// Check says why the preset cannot work on this input, or returns nil.
	Check func(Context) error
	// Fields returns the form for the current values. It can change as
	// values change, such as offering only the encoders for the chosen
	// codec.
	Fields func(Context, Values) []Field
	// Ext is the output's extension, with its dot, for the current values.
	Ext func(Context, Values) string
	// Build returns what to run. It reports answers that do not work, such
	// as a start time after the end, as errors.
	Build func(c Context, v Values, output string) (Plan, error)
}

// EncoderKey is the key of the field that picks a video encoder. Presets
// with hardware encoders use it, so the UI can offer a software retry.
const EncoderKey = "encoder"

// All returns every preset, in menu order.
func All() []Preset {
	return []Preset{transcode, remux, compress, extractAudio, trim, resize, gif, chooseStreams}
}

// Fill gives every field a valid value: a missing value, or a choice that is
// no longer offered, becomes the field's default. Because fields can depend
// on values, it repeats until the form stops changing, and returns the
// fields for the final values.
func Fill(p Preset, c Context, v Values) (Values, []Field) {
	v = cloneValues(v)
	var fields []Field
	for range 5 {
		fields = p.Fields(c, v)
		changed := false
		for _, f := range fields {
			cur, ok := v[f.Key]
			valid := ok && (f.Kind == Text || slices.ContainsFunc(f.Options, func(o Option) bool { return o.Value == cur }))
			if !valid {
				changed = changed || !ok || cur != f.Default
				v[f.Key] = f.Default
			}
		}
		if !changed {
			break
		}
	}
	return v, fields
}

func cloneValues(v Values) Values {
	out := make(Values, len(v))
	for k, x := range v {
		out[k] = x
	}
	return out
}

// DefaultOutput is where the output goes unless the user says otherwise:
// next to the input, or in dir, named after the input with the preset's
// suffix, as in "clip-small.mp4". It can be the input itself, which the
// caller must refuse.
func DefaultOutput(p Preset, c Context, v Values, dir string) string {
	base := filepath.Base(c.Input)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if p.Suffix != "" {
		name += "-" + p.Suffix
	}
	name += p.Ext(c, v)
	if dir == "" {
		dir = filepath.Dir(c.Input)
	}
	return filepath.Join(dir, name)
}

// ParseTime reads a time such as "90", "1:30", "1:30.5", or "0:01:30".
func ParseTime(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if s == "" || len(parts) > 3 {
		return 0, fmt.Errorf("%q is not a time; use seconds or H:MM:SS", s)
	}
	var total float64
	for i, p := range parts {
		n, err := strconv.ParseFloat(p, 64)
		// Only the last part can have a fraction, and minutes and seconds
		// after a colon must be below 60.
		if err != nil || n < 0 || i < len(parts)-1 && strings.Contains(p, ".") || i > 0 && n >= 60 {
			return 0, fmt.Errorf("%q is not a time; use seconds or H:MM:SS", s)
		}
		total = total*60 + n
	}
	return time.Duration(total * float64(time.Second)).Round(time.Millisecond), nil
}

// clock writes d as H:MM:SS.mmm, the format ParseTime reads and FFmpeg
// prints.
func clock(d time.Duration) string {
	d = d.Round(time.Millisecond)
	return fmt.Sprintf("%d:%02d:%02d.%03d", d/time.Hour, d%time.Hour/time.Minute, d%time.Minute/time.Second, d%time.Second/time.Millisecond)
}

// seconds writes d for FFmpeg's -ss and -t, such as "12.5".
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}

// videoStreams returns the input's video streams, without cover art.
func videoStreams(c Context) []ffmpeg.Stream {
	var out []ffmpeg.Stream
	for _, s := range c.Media.Streams {
		if s.Type == "video" && !s.CoverArt {
			out = append(out, s)
		}
	}
	return out
}

func audioStreams(c Context) []ffmpeg.Stream {
	var out []ffmpeg.Stream
	for _, s := range c.Media.Streams {
		if s.Type == "audio" {
			out = append(out, s)
		}
	}
	return out
}

func needVideo(c Context) error {
	if len(videoStreams(c)) == 0 {
		return fmt.Errorf("the file has no video")
	}
	return nil
}

func needDuration(c Context) error {
	if c.Media.Duration <= 0 {
		return fmt.Errorf("FFmpeg cannot tell how long the file is")
	}
	return nil
}

func all(checks ...func(Context) error) func(Context) error {
	return func(c Context) error {
		for _, check := range checks {
			if err := check(c); err != nil {
				return err
			}
		}
		return nil
	}
}

func choice(key, label, def string, opts ...Option) Field {
	return Field{Key: key, Label: label, Kind: Choice, Options: opts, Default: def}
}

func text(key, label, def, hint string) Field {
	return Field{Key: key, Label: label, Kind: Text, Default: def, Hint: hint}
}

func opt(value, label string) Option { return Option{Value: value, Label: label} }
