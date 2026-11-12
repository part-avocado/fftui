package preset

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

var (
	qualityField = choice("quality", "Quality", "medium",
		opt("high", "High (bigger file)"), opt("medium", "Medium"), opt("low", "Low (smaller file)"))
	speedField = choice("speed", "Speed", "medium",
		opt("fast", "Fast (bigger file)"), opt("medium", "Medium"), opt("slow", "Slow (smaller file)"))
)

// encoderFields returns the Encoder field for codec and, when the chosen
// encoder has one, the Speed field.
func encoderFields(c Context, v Values, codec string) []Field {
	encs := videoEncoders(c, codec)
	if len(encs) == 0 {
		return nil
	}
	// The software encoder comes first, so every form starts on it.
	fields := []Field{choice(EncoderKey, "Encoder", encs[0].Value, encs...)}
	if speedArgs(c, v[EncoderKey], "medium") != nil {
		fields = append(fields, speedField)
	}
	return fields
}

// audioField offers keeping the audio, converting it with an encoder cont
// can hold, or leaving it out. It is nil when the input has no audio.
func audioField(c Context, cont container) []Field {
	if len(audioStreams(c)) == 0 {
		return nil
	}
	opts := []Option{opt("copy", "Keep as it is")}
	for _, enc := range []string{"aac", "libopus", "libvorbis"} {
		codec := map[string]string{"aac": "aac", "libopus": "opus", "libvorbis": "vorbis"}[enc]
		// MKV holds anything, but AAC and Opus are enough to offer.
		if c.Caps.HasEncoder(enc) && (cont.audio == nil && enc != "libvorbis" || cont.audio != nil && holds(cont.audio, codec)) {
			opts = append(opts, opt(enc, "Convert with "+enc))
		}
	}
	return []Field{choice("audio", "Audio", "copy", append(opts, opt("none", "No audio"))...)}
}

func subtitlesField(c Context) []Field {
	if !slices.ContainsFunc(c.Media.Streams, func(s ffmpeg.Stream) bool { return s.Type == "subtitle" }) {
		return nil
	}
	return []Field{choice("subtitles", "Subtitles", "keep", opt("keep", "Keep"), opt("drop", "Leave out"))}
}

func needEncoder(codecs ...string) func(Context) error {
	return func(c Context) error {
		for _, codec := range codecs {
			if len(videoEncoders(c, codec)) > 0 {
				return nil
			}
		}
		return fmt.Errorf("this FFmpeg has no %s encoder", strings.Join(codecs, " or "))
	}
}

func single(args, notes []string, err error, d time.Duration) (Plan, error) {
	return Plan{Steps: [][]string{args}, Notes: notes, Duration: d}, err
}

var transcode = Preset{
	ID:          "convert",
	Name:        "Convert video",
	Description: "Re-encode with another codec, such as H.264, H.265, or AV1",
	Suffix:      "converted",
	Check:       all(needVideo, needEncoder("h264", "hevc", "av1", "vp9")),
	Fields: func(c Context, v Values) []Field {
		var conts []Option
		for _, id := range []string{"mp4", "mkv", "webm", "mov"} {
			if len(codecOptions(c, containers[id])) > 0 {
				conts = append(conts, opt(id, containers[id].name))
			}
		}
		cont := containers[v["container"]]
		codecs := codecOptions(c, cont)
		fields := []Field{choice("container", "Container", conts[0].Value, conts...)}
		if len(codecs) > 0 {
			fields = append(fields, choice("codec", "Codec", codecs[0].Value, codecs...))
		}
		fields = append(fields, encoderFields(c, v, v["codec"])...)
		fields = append(fields, qualityField)
		fields = append(fields, audioField(c, cont)...)
		return append(fields, subtitlesField(c)...)
	},
	Ext: func(_ Context, v Values) string { return containers[v["container"]].ext },
	Build: func(c Context, v Values, out string) (Plan, error) {
		j := videoJob{c: c, cont: containers[v["container"]], encoder: v[EncoderKey], quality: v["quality"],
			speed: v["speed"], audio: v["audio"], keepSubs: v["subtitles"] != "drop"}
		args, notes, err := j.build(out)
		return single(args, notes, err, c.Media.Duration)
	},
}

var remux = Preset{
	ID:          "remux",
	Name:        "Change container",
	Description: "Put the same video in MP4, MKV, MOV, or WebM without re-encoding it",
	Suffix:      "remux",
	Check:       needVideo,
	Fields: func(c Context, v Values) []Field {
		var opts []Option
		def := ""
		for _, id := range []string{"mp4", "mkv", "mov", "webm"} {
			cont := containers[id]
			if !slices.ContainsFunc(videoStreams(c), func(s ffmpeg.Stream) bool { return !holds(cont.video, s.Codec) }) {
				opts = append(opts, opt(id, cont.name))
				if def == "" && cont.ext != inputExt(c) {
					def = id
				}
			}
		}
		return []Field{choice("container", "Container", def, opts...)}
	},
	Ext: func(_ Context, v Values) string { return containers[v["container"]].ext },
	Build: func(c Context, v Values, out string) (Plan, error) {
		j := videoJob{c: c, cont: containers[v["container"]], encoder: "copy", audio: "copy", keepSubs: true}
		args, notes, err := j.build(out)
		return single(args, notes, err, c.Media.Duration)
	},
}

var compress = Preset{
	ID:          "compress",
	Name:        "Compress to a size",
	Description: "Re-encode to fit a file size, such as an upload limit",
	Suffix:      "small",
	Check:       all(needVideo, needDuration, needEncoder("h264", "hevc")),
	Fields: func(c Context, v Values) []Field {
		half := float64(c.Media.Size) / 2 / 1e6
		def := "10"
		if half > 0 {
			def = strconv.FormatFloat(max(0.1, math.Round(half*10)/10), 'f', -1, 64)
		}
		fields := []Field{text("size", "Target size", def, "MB")}
		var opts []Option
		for _, o := range codecs[:2] {
			if len(videoEncoders(c, o.Value)) > 0 {
				opts = append(opts, o)
			}
		}
		fields = append(fields, choice("codec", "Codec", opts[0].Value, opts...))
		fields = append(fields, encoderFields(c, v, v["codec"])...)
		if v[EncoderKey] == "libx264" {
			fields = append(fields, choice("passes", "Passes", "2",
				opt("2", "Two (closer to the size)"), opt("1", "One (faster)")))
		}
		if len(audioStreams(c)) > 0 {
			fields = append(fields, choice("audio", "Audio", "128k",
				opt("64k", "64 kb/s"), opt("96k", "96 kb/s"), opt("128k", "128 kb/s"), opt("192k", "192 kb/s"), opt("none", "No audio")))
		}
		return fields
	},
	Ext: func(Context, Values) string { return ".mp4" },
	Build: func(c Context, v Values, out string) (Plan, error) {
		size, err := strconv.ParseFloat(strings.TrimSpace(v["size"]), 64)
		if err != nil || size <= 0 || math.IsInf(size, 0) {
			return Plan{}, fmt.Errorf("the target size must be a number of MB, such as 25")
		}
		dur := c.Media.Duration.Seconds()
		var audioBits float64
		audio := "none"
		if n := len(audioStreams(c)); n > 0 && v["audio"] != "none" {
			kbps, _ := strconv.Atoi(strings.TrimSuffix(v["audio"], "k"))
			audioBits, audio = float64(kbps*1000*n), "aac"
		}
		// Leave 3% for the container.
		const overhead, minVideo = 0.97, 100_000
		videoBits := size*1e6*8*overhead/dur - audioBits
		if videoBits < minVideo {
			need := (minVideo + audioBits) * dur / 8 / 1e6 / overhead
			return Plan{}, fmt.Errorf("%s MB is too small for %s of video; it needs at least %.1f MB", v["size"], clock(c.Media.Duration), math.Ceil(need*10)/10)
		}
		j := videoJob{c: c, cont: containers["mp4"], encoder: v[EncoderKey], speed: v["speed"],
			bitrate: int64(videoBits), audio: audio, audioBitrate: v["audio"]}
		var notes []string
		if size*1e6 >= float64(c.Media.Size) && c.Media.Size > 0 {
			notes = append(notes, fmt.Sprintf("The file is already %.1f MB, so this will not make it smaller.", float64(c.Media.Size)/1e6))
		}
		if v[EncoderKey] != "libx264" || v["passes"] != "2" {
			args, more, err := j.build(out)
			return single(args, append(notes, more...), err, c.Media.Duration)
		}
		j.passlog = c.Work + "-pass"
		j.pass = 1
		first, _, err := j.build(out)
		if err != nil {
			return Plan{}, err
		}
		j.pass = 2
		second, more, err := j.build(out)
		return Plan{Steps: [][]string{first, second}, Duration: c.Media.Duration, Notes: append(notes, more...),
			Temp: []string{j.passlog + "*"}}, err
	},
}

// audioFormats are the formats Extract audio offers: encoder, extension,
// and quality settings for high, medium, and low.
var audioFormats = []struct {
	value, label, encoder, ext string
	quality                    [3][]string
}{
	{"mp3", "MP3", "libmp3lame", ".mp3", [3][]string{{"-q:a", "0"}, {"-q:a", "2"}, {"-q:a", "5"}}},
	{"aac", "AAC (.m4a)", "aac", ".m4a", [3][]string{{"-b:a", "256k"}, {"-b:a", "192k"}, {"-b:a", "128k"}}},
	{"opus", "Opus", "libopus", ".opus", [3][]string{{"-b:a", "160k"}, {"-b:a", "128k"}, {"-b:a", "96k"}}},
	{"flac", "FLAC (lossless)", "flac", ".flac", [3][]string{}},
	{"wav", "WAV (uncompressed)", "pcm_s16le", ".wav", [3][]string{}},
}

// copyExt is the extension for copying an audio codec as it is.
func copyExt(codec string) string {
	switch {
	case codec == "aac" || codec == "alac":
		return ".m4a"
	case codec == "mp3" || codec == "flac" || codec == "ac3" || codec == "eac3":
		return "." + codec
	case codec == "opus":
		return ".opus"
	case codec == "vorbis":
		return ".ogg"
	case strings.HasPrefix(codec, "pcm_") && strings.HasSuffix(codec, "le"):
		return ".wav"
	}
	return ".mka"
}

func needAudio(c Context) error {
	if len(audioStreams(c)) == 0 {
		return fmt.Errorf("the file has no audio")
	}
	return nil
}

// track returns the audio stream the user chose.
func track(c Context, v Values) ffmpeg.Stream {
	auds := audioStreams(c)
	for _, s := range auds {
		if strconv.Itoa(s.Index) == v["track"] {
			return s
		}
	}
	return auds[0]
}

var extractAudio = Preset{
	ID:          "audio",
	Name:        "Extract audio",
	Description: "Save the sound as MP3, AAC, Opus, FLAC, or WAV",
	Suffix:      "audio",
	Check:       needAudio,
	Fields: func(c Context, v Values) []Field {
		var fields []Field
		if auds := audioStreams(c); len(auds) > 1 {
			opts := make([]Option, len(auds))
			def := strconv.Itoa(auds[0].Index)
			for i, s := range auds {
				opts[i] = opt(strconv.Itoa(s.Index), describe(s))
				if s.Default && def == strconv.Itoa(auds[0].Index) {
					def = opts[i].Value
				}
			}
			fields = append(fields, choice("track", "Track", def, opts...))
		}
		opts := []Option{opt("copy", "Original ("+track(c, v).Codec+", no conversion)")}
		def := "copy"
		for _, f := range audioFormats {
			if c.Caps.HasEncoder(f.encoder) {
				opts = append(opts, opt(f.value, f.label))
				if def == "copy" {
					def = f.value
				}
			}
		}
		fields = append(fields, choice("format", "Format", def, opts...))
		for _, f := range audioFormats {
			if f.value == v["format"] && len(f.quality[0]) > 0 {
				fields = append(fields, qualityField)
			}
		}
		return fields
	},
	Ext: func(c Context, v Values) string {
		for _, f := range audioFormats {
			if f.value == v["format"] {
				return f.ext
			}
		}
		return copyExt(track(c, v).Codec)
	},
	Build: func(c Context, v Values, out string) (Plan, error) {
		args := []string{"-i", c.Input, "-map", "0:" + strconv.Itoa(track(c, v).Index)}
		if v["format"] == "copy" {
			args = append(args, "-c:a", "copy")
		}
		for _, f := range audioFormats {
			if f.value == v["format"] {
				args = append(args, "-c:a", f.encoder)
				if len(f.quality[0]) > 0 {
					args = append(args, f.quality[levelIndex(v["quality"])]...)
				}
			}
		}
		return single(append(args, out), nil, nil, c.Media.Duration)
	},
}

// trimVideo and trimAudio map the input extensions Trim keeps to their
// container or audio encoder. Other inputs are cut to MKV.
var (
	trimVideo = map[string]string{".mp4": "mp4", ".m4v": "mp4", ".mov": "mov", ".mkv": "mkv", ".webm": "webm"}
	trimAudio = map[string]string{".mp3": "libmp3lame", ".m4a": "aac", ".flac": "flac", ".wav": "pcm_s16le", ".ogg": "libvorbis", ".opus": "libopus"}
)

func inputExt(c Context) string { return strings.ToLower(filepath.Ext(c.Input)) }

// span reads the start and end fields and checks them against the input.
func span(c Context, v Values, startKey, endKey string) (start, length time.Duration, err error) {
	start, err = ParseTime(v[startKey])
	if err != nil {
		return 0, 0, fmt.Errorf("start: %w", err)
	}
	end, err := ParseTime(v[endKey])
	if err != nil {
		return 0, 0, fmt.Errorf("end: %w", err)
	}
	switch {
	case end <= start:
		return 0, 0, errors.New("the end must be after the start")
	case end > c.Media.Duration+time.Millisecond:
		return 0, 0, fmt.Errorf("the end is after the end of the file, %s", clock(c.Media.Duration))
	}
	return start, end - start, nil
}

var trim = Preset{
	ID:          "trim",
	Name:        "Trim",
	Description: "Keep the part between two times",
	Suffix:      "trim",
	Check:       needDuration,
	Fields: func(c Context, v Values) []Field {
		return []Field{
			text("start", "Start", "0:00:00.000", "H:MM:SS or seconds"),
			text("end", "End", clock(c.Media.Duration), "H:MM:SS or seconds"),
			choice("mode", "Cut", "copy",
				opt("copy", "Fast (no re-encoding; starts at a keyframe)"),
				opt("exact", "Exact (re-encodes)")),
		}
	},
	Ext: func(c Context, _ Values) string {
		switch ext := inputExt(c); {
		case ext == ".m4v":
			return ".mp4" // FFmpeg reads .m4v as raw MPEG-4 video
		case trimVideo[ext] != "" || trimAudio[ext] != "":
			return ext
		}
		return ".mkv"
	},
	Build: func(c Context, v Values, out string) (Plan, error) {
		start, length, err := span(c, v, "start", "end")
		if err != nil {
			return Plan{}, err
		}
		pre := []string{"-ss", seconds(start)}
		post := []string{"-t", seconds(length)}
		ext := strings.ToLower(filepath.Ext(out))
		if v["mode"] == "copy" {
			args := append(pre, "-i", c.Input, "-map", "0:v?", "-map", "0:a?", "-map", "0:s?")
			if ext == ".mkv" {
				args = append(args, "-map", "0:t?")
			}
			args = append(args, "-c", "copy", "-avoid_negative_ts", "make_zero")
			args = append(append(args, post...), out)
			notes := []string{"Fast cuts start at the keyframe at or before the start, so the clip can begin a little early."}
			return Plan{Steps: [][]string{args}, Duration: length, Notes: notes}, nil
		}
		if len(videoStreams(c)) == 0 {
			enc := trimAudio[ext]
			if enc == "" || !c.Caps.HasEncoder(enc) {
				return Plan{}, errors.New("exact cuts are not supported for this format; use a fast cut")
			}
			args := append(pre, "-i", c.Input, "-map", "0:a", "-c:a", enc)
			if b := audioBitrates[enc]; b != "" {
				args = append(args, "-b:a", b)
			}
			return single(append(append(args, post...), out), nil, nil, length)
		}
		cont := containers[trimVideo[ext]]
		if cont.name == "" {
			cont = containers["mkv"]
		}
		enc := "libx264"
		if cont.name == "WebM" {
			enc = "libvpx-vp9"
		}
		if !c.Caps.HasEncoder(enc) {
			return Plan{}, fmt.Errorf("exact cuts to %s need %s, which this FFmpeg does not have", cont.name, enc)
		}
		audio, err := firstEncoder(c, cont.defaultAudio)
		if err != nil {
			return Plan{}, err
		}
		j := videoJob{c: c, cont: cont, encoder: enc, quality: "high", speed: "medium", audio: audio, keepSubs: true, pre: pre, post: post}
		args, notes, err := j.build(out)
		return single(args, notes, err, length)
	},
}

var resize = Preset{
	ID:          "resize",
	Name:        "Resize",
	Description: "Make the picture smaller or lower the frame rate",
	Suffix:      "resized",
	Check: all(needVideo, needEncoder("h264"), func(c Context) error {
		if h := videoStreams(c)[0].Height; h <= 240 {
			return fmt.Errorf("the video is only %d pixels tall", h)
		}
		return nil
	}),
	Fields: func(c Context, v Values) []Field {
		src := videoStreams(c)[0]
		var heights []Option
		def := ""
		for _, h := range []int{2160, 1440, 1080, 720, 480, 360, 240} {
			if h < src.Height {
				heights = append(heights, opt(strconv.Itoa(h), fmt.Sprintf("%dp", h)))
				if def == "" || h == 720 {
					def = strconv.Itoa(h)
				}
			}
		}
		rates := []Option{opt("keep", "Keep")}
		if src.FrameRate > 0 {
			rates[0].Label = "Keep (" + strconv.FormatFloat(math.Round(src.FrameRate*100)/100, 'f', -1, 64) + " fps)"
		}
		for _, r := range []int{60, 30, 24} {
			if float64(r) < src.FrameRate-0.5 {
				rates = append(rates, opt(strconv.Itoa(r), fmt.Sprintf("%d fps", r)))
			}
		}
		fields := []Field{choice("height", "Height", def, heights...), choice("fps", "Frame rate", "keep", rates...)}
		fields = append(fields, encoderFields(c, v, "h264")...)
		fields = append(fields, qualityField)
		return append(fields, audioField(c, containers["mp4"])...)
	},
	Ext: func(Context, Values) string { return ".mp4" },
	Build: func(c Context, v Values, out string) (Plan, error) {
		src := videoStreams(c)[0]
		h, err := strconv.Atoi(v["height"])
		if err != nil {
			return Plan{}, errors.New("choose a height")
		}
		j := videoJob{c: c, cont: containers["mp4"], encoder: v[EncoderKey], quality: v["quality"], speed: v["speed"],
			audio: v["audio"], keepSubs: true, filters: []string{"scale=-2:" + v["height"]}, height: h}
		if src.Height > 0 {
			j.width = int(math.Round(float64(src.Width)*float64(h)/float64(src.Height)/2)) * 2
		}
		if v["fps"] != "keep" {
			j.filters = append(j.filters, "fps="+v["fps"])
			j.fps, _ = strconv.ParseFloat(v["fps"], 64)
		}
		args, notes, err := j.build(out)
		return single(args, notes, err, c.Media.Duration)
	},
}

var gif = Preset{
	ID:          "gif",
	Name:        "Make a GIF",
	Description: "Turn a few seconds into an animated GIF",
	Suffix:      "",
	Check:       all(needVideo, needDuration),
	Fields: func(c Context, v Values) []Field {
		src := videoStreams(c)[0]
		var widths []Option
		def := ""
		for _, w := range []int{320, 480, 640, 800} {
			if w <= src.Width || src.Width == 0 {
				widths = append(widths, opt(strconv.Itoa(w), fmt.Sprintf("%d pixels", w)))
				if def == "" || w == 480 {
					def = strconv.Itoa(w)
				}
			}
		}
		if len(widths) == 0 {
			def = strconv.Itoa(src.Width)
			widths = []Option{opt(def, def+" pixels (as now)")}
		}
		length := min(5*time.Second, c.Media.Duration)
		return []Field{
			text("start", "Start", "0:00:00", "H:MM:SS or seconds"),
			text("length", "Length", seconds(length.Round(100*time.Millisecond)), "seconds"),
			choice("width", "Width", def, widths...),
			choice("fps", "Frame rate", "15", opt("10", "10 fps"), opt("15", "15 fps"), opt("20", "20 fps"), opt("25", "25 fps")),
		}
	},
	Ext: func(Context, Values) string { return ".gif" },
	Build: func(c Context, v Values, out string) (Plan, error) {
		start, err := ParseTime(v["start"])
		if err != nil {
			return Plan{}, fmt.Errorf("start: %w", err)
		}
		length, err := ParseTime(v["length"])
		if err != nil || length <= 0 {
			return Plan{}, errors.New("the length must be a number of seconds, such as 5")
		}
		if start >= c.Media.Duration {
			return Plan{}, fmt.Errorf("the start is after the end of the file, %s", clock(c.Media.Duration))
		}
		length = min(length, c.Media.Duration-start)
		var notes []string
		if length > 15*time.Second {
			notes = append(notes, "Long GIFs get big; a short video is often much smaller.")
		}
		// One run makes the best 256-color palette for the clip, then
		// draws the clip with it.
		filter := fmt.Sprintf("fps=%s,scale=%s:-1:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse", v["fps"], v["width"])
		args := []string{"-ss", seconds(start), "-t", seconds(length), "-i", c.Input,
			"-map", "0:" + strconv.Itoa(videoStreams(c)[0].Index), "-vf", filter, "-loop", "0", out}
		return Plan{Steps: [][]string{args}, Duration: length, Notes: notes}, nil
	},
}

var chooseStreams = Preset{
	ID:          "streams",
	Name:        "Choose streams",
	Description: "Keep only some of the tracks, such as one audio language",
	Suffix:      "streams",
	Check: func(c Context) error {
		if len(c.Media.Streams) < 2 {
			return errors.New("the file has only one stream")
		}
		return nil
	},
	Fields: func(c Context, _ Values) []Field {
		var fields []Field
		for _, s := range c.Media.Streams {
			f := choice(streamKey(s), fmt.Sprintf("#%d", s.Index), "keep", opt("keep", "Keep: "+describe(s)), opt("drop", "Leave out: "+describe(s)))
			fields = append(fields, f)
		}
		return fields
	},
	Ext: func(c Context, _ Values) string {
		if trimVideo[inputExt(c)] != "" || trimAudio[inputExt(c)] != "" {
			return inputExt(c)
		}
		return ".mkv"
	},
	Build: func(c Context, v Values, out string) (Plan, error) {
		args := []string{"-i", c.Input}
		cont := containers[trimVideo[strings.ToLower(filepath.Ext(out))]]
		convert := false
		for _, s := range c.Media.Streams {
			if v[streamKey(s)] == "drop" {
				continue
			}
			args = append(args, "-map", "0:"+strconv.Itoa(s.Index))
			if s.Type == "subtitle" && cont.subs != "" && cont.subs != "copy" {
				if !slices.Contains(textSubtitles, s.Codec) {
					return Plan{}, fmt.Errorf("stream #%d is picture-based subtitles, which cannot go in %s; leave it out or save as .mkv", s.Index, cont.name)
				}
				convert = true
			}
		}
		if len(args) == 2 {
			return Plan{}, errors.New("keep at least one stream")
		}
		args = append(args, "-c", "copy")
		if convert {
			args = append(args, "-c:s", cont.subs)
		}
		if cont.faststart {
			args = append(args, "-movflags", "+faststart")
		}
		return single(append(args, out), nil, nil, c.Media.Duration)
	},
}

func streamKey(s ffmpeg.Stream) string { return "stream" + strconv.Itoa(s.Index) }

// describe sums a stream up in a few words, such as "audio opus 5.1 eng".
func describe(s ffmpeg.Stream) string {
	parts := []string{s.Type, s.Codec}
	switch {
	case s.CoverArt:
		parts = append(parts, "cover art")
	case s.Type == "video" && s.Width > 0:
		parts = append(parts, fmt.Sprintf("%dx%d", s.Width, s.Height))
	case s.Type == "audio" && s.ChannelLayout != "":
		parts = append(parts, s.ChannelLayout)
	case s.Type == "audio" && s.Channels > 0:
		parts = append(parts, fmt.Sprintf("%d ch", s.Channels))
	}
	if s.Language != "" {
		parts = append(parts, s.Language)
	}
	if s.Title != "" {
		parts = append(parts, strconv.Quote(s.Title))
	}
	if s.Forced {
		parts = append(parts, "forced")
	}
	return strings.Join(slices.DeleteFunc(parts, func(p string) bool { return p == "" }), " ")
}
