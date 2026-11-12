package ffmpeg

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Capabilities is what the local FFmpeg build can do, read once at startup.
type Capabilities struct {
	Version  Version
	Encoders []Codec
	Decoders []Codec
	// Muxers are the output format names, such as "mp4" and "matroska".
	Muxers []string
	// HWAccels are the hardware decoding methods, such as "cuda" and "vaapi".
	HWAccels []string
}

// Codec is one encoder or decoder from `ffmpeg -encoders` or `-decoders`.
type Codec struct {
	Name string // what -c:v takes, such as "libx264"
	// Kind is 'V' for video, 'A' for audio, or 'S' for subtitles.
	Kind byte
	// Codec is the format it reads or writes, such as "h264" for libx264.
	Codec       string
	Description string
}

// hardwareAPIs maps the suffix FFmpeg gives hardware codecs to the name of
// the API, so "h264_nvenc" is NVIDIA NVENC.
var hardwareAPIs = map[string]string{
	"nvenc":        "NVIDIA NVENC",
	"cuvid":        "NVIDIA CUVID",
	"qsv":          "Intel Quick Sync",
	"vaapi":        "VA-API",
	"videotoolbox": "Apple VideoToolbox",
	"amf":          "AMD AMF",
	"vulkan":       "Vulkan",
	"d3d12va":      "Direct3D 12",
	"mf":           "Media Foundation",
	"v4l2m2m":      "V4L2 M2M",
	"omx":          "OpenMAX",
	"mediacodec":   "Android MediaCodec",
	"rkmpp":        "Rockchip MPP",
}

// HardwareAPI returns the hardware API the codec uses, such as "NVIDIA NVENC",
// or "" for a software codec.
func (c Codec) HardwareAPI() string {
	i := strings.LastIndexByte(c.Name, '_')
	if i < 0 {
		return ""
	}
	return hardwareAPIs[c.Name[i+1:]]
}

// HasEncoder reports whether the build has the named encoder.
func (c Capabilities) HasEncoder(name string) bool {
	return slices.ContainsFunc(c.Encoders, func(e Codec) bool { return e.Name == name })
}

// HardwareEncoders returns the encoders that use a hardware API. Having one
// only means FFmpeg was built with it; the device and driver are checked when
// a job starts.
func (c Capabilities) HardwareEncoders() []Codec {
	var hw []Codec
	for _, e := range c.Encoders {
		if e.HardwareAPI() != "" {
			hw = append(hw, e)
		}
	}
	return hw
}

// Detect runs ffmpeg to read its version and what it supports.
func Detect(ctx context.Context, r Runner, ffmpeg string) (Capabilities, error) {
	var c Capabilities
	run := func(flag string) (string, error) {
		out, err := r.Output(ctx, ffmpeg, "-hide_banner", flag)
		return string(out), err
	}
	out, err := run("-version")
	if err != nil {
		return c, err
	}
	if c.Version, err = parseVersion(out); err != nil {
		return c, fmt.Errorf("%s: %w", ffmpeg, err)
	}
	if out, err = run("-encoders"); err != nil {
		return c, err
	}
	c.Encoders = parseCodecs(out)
	if out, err = run("-decoders"); err != nil {
		return c, err
	}
	c.Decoders = parseCodecs(out)
	if out, err = run("-muxers"); err != nil {
		return c, err
	}
	c.Muxers = parseMuxers(out)
	if out, err = run("-hwaccels"); err != nil {
		return c, err
	}
	c.HWAccels = parseHWAccels(out)
	return c, nil
}

// Version is FFmpeg's version. Builds from git, such as "N-117045-g1a2b3c",
// have no release number; Known is false for them.
type Version struct {
	// String is the version as FFmpeg prints it, such as "7.1.1" or
	// "6.1.1-full_build-www.gyan.dev".
	String       string
	Major, Minor int
	Known        bool
}

// AtLeast reports whether v is major.minor or newer. Git builds are treated
// as newer than every release, since they are almost always recent.
func (v Version) AtLeast(major, minor int) bool {
	if !v.Known {
		return true
	}
	return v.Major > major || v.Major == major && v.Minor >= minor
}

var releaseRE = regexp.MustCompile(`^n?(\d+)\.(\d+)`)

func parseVersion(out string) (Version, error) {
	first := strings.TrimSpace(splitLines(out)[0])
	rest, ok := strings.CutPrefix(first, "ffmpeg version ")
	if !ok {
		return Version{}, fmt.Errorf("not an ffmpeg version line: %q", first)
	}
	v := Version{String: strings.Fields(rest + " ")[0]}
	if m := releaseRE.FindStringSubmatch(v.String); m != nil {
		v.Major, _ = strconv.Atoi(m[1])
		v.Minor, _ = strconv.Atoi(m[2])
		v.Known = true
	}
	return v, nil
}

// listing returns the lines after the row of dashes that ends the legend in
// `ffmpeg -encoders`, `-decoders`, and `-muxers`.
func listing(out string) []string {
	lines := splitLines(out)
	for i, line := range lines {
		if t := strings.TrimSpace(line); t != "" && strings.Trim(t, "-") == "" {
			return lines[i+1:]
		}
	}
	return nil
}

// parseCodecs reads lines such as
//
//	V....D libx264              libx264 H.264 / AVC (codec h264)
//
// FFmpeg only prints "(codec x)" when the codec's name differs from the
// encoder's, so a missing one means they are the same.
func parseCodecs(out string) []Codec {
	var codecs []Codec
	for _, line := range listing(out) {
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields[0]) != 6 {
			continue
		}
		c := Codec{Name: fields[1], Kind: fields[0][0], Codec: fields[1]}
		desc := strings.TrimSpace(line)[len(fields[0]):]
		desc = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(desc), fields[1]))
		if i := strings.LastIndex(desc, " (codec "); i >= 0 && strings.HasSuffix(desc, ")") {
			c.Codec = desc[i+len(" (codec ") : len(desc)-1]
			desc = desc[:i]
		}
		c.Description = desc
		codecs = append(codecs, c)
	}
	return codecs
}

// parseMuxers reads lines such as "  E  mp4  MP4 (MPEG-4 Part 14)". A line
// can name several formats separated by commas, such as "sdl,sdl2".
func parseMuxers(out string) []string {
	var names []string
	for _, line := range listing(out) {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(fields[0], "E") {
			continue
		}
		names = append(names, strings.Split(fields[1], ",")...)
	}
	return names
}

func parseHWAccels(out string) []string {
	var names []string
	lines := splitLines(out)
	for i, line := range lines {
		if strings.HasPrefix(line, "Hardware acceleration methods:") {
			for _, name := range lines[i+1:] {
				if name = strings.TrimSpace(name); name != "" {
					names = append(names, name)
				}
			}
			break
		}
	}
	return names
}
