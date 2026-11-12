package preset

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/part-avocado/fftui/internal/ffmpeg"
)

// container is an output format and what it can hold.
type container struct {
	name string // shown to the user
	ext  string
	// video and audio list the codecs it can hold; nil means any.
	video, audio []string
	// defaultAudio are the encoders to convert audio to when it does not
	// fit, best first.
	defaultAudio []string
	// subs is the encoder for text subtitles, "copy" to keep every
	// subtitle as it is, or "" when none fit.
	subs        string
	faststart   bool // move the index to the front, so playback can start before the download ends
	attachments bool // can hold attached files, such as fonts for subtitles
}

var containers = map[string]container{
	"mp4": {name: "MP4", ext: ".mp4",
		video:        []string{"h264", "hevc", "av1", "vp9", "mpeg4", "mpeg2video", "mpeg1video", "mjpeg"},
		audio:        []string{"aac", "mp3", "ac3", "eac3", "alac", "mp2"},
		defaultAudio: []string{"aac"}, subs: "mov_text", faststart: true},
	"mov": {name: "MOV", ext: ".mov",
		video:        []string{"h264", "hevc", "mpeg4", "prores", "mjpeg", "mpeg2video", "dnxhd", "png", "qtrle"},
		audio:        []string{"aac", "mp3", "alac", "ac3", "pcm_s16le", "pcm_s24le", "pcm_s32le", "pcm_f32le", "pcm_s16be", "pcm_s24be"},
		defaultAudio: []string{"aac"}, subs: "mov_text", faststart: true},
	"mkv": {name: "MKV", ext: ".mkv", defaultAudio: []string{"aac"}, subs: "copy", attachments: true},
	"webm": {name: "WebM", ext: ".webm",
		video:        []string{"vp8", "vp9", "av1"},
		audio:        []string{"opus", "vorbis"},
		defaultAudio: []string{"libopus", "libvorbis"}, subs: "webvtt"},
}

func holds(list []string, codec string) bool { return list == nil || slices.Contains(list, codec) }

// textSubtitles are subtitle codecs stored as text, which can be converted
// to another text format. The others, such as Blu-ray's, are pictures.
var textSubtitles = []string{"subrip", "ass", "ssa", "webvtt", "mov_text", "text"}

// codecs are the video codecs fftui encodes to, in menu order.
var codecs = []Option{opt("h264", "H.264"), opt("hevc", "H.265 (HEVC)"), opt("av1", "AV1"), opt("vp9", "VP9")}

var softwareEncoders = map[string][]string{
	"h264": {"libx264"},
	"hevc": {"libx265"},
	"vp9":  {"libvpx-vp9"},
	"av1":  {"libsvtav1", "libaom-av1"},
}

// videoEncoders returns the build's encoders for codec: the software ones
// first, then every hardware one.
func videoEncoders(c Context, codec string) []Option {
	var opts []Option
	for _, name := range softwareEncoders[codec] {
		if c.Caps.HasEncoder(name) {
			opts = append(opts, opt(name, name+" (software)"))
		}
	}
	for _, e := range c.Caps.HardwareEncoders() {
		if e.Codec == codec {
			opts = append(opts, opt(e.Name, e.Name+" ("+e.HardwareAPI()+")"))
		}
	}
	return opts
}

// codecOptions returns the codecs that cont can hold and the build can
// encode.
func codecOptions(c Context, cont container) []Option {
	var opts []Option
	for _, o := range codecs {
		if holds(cont.video, o.Value) && len(videoEncoders(c, o.Value)) > 0 {
			opts = append(opts, o)
		}
	}
	return opts
}

// hardwareKind returns the suffix of a hardware encoder, such as "nvenc"
// for "h264_nvenc", or "" for a software encoder.
func hardwareKind(enc string) string {
	if (ffmpeg.Codec{Name: enc}).HardwareAPI() == "" {
		return ""
	}
	return enc[strings.LastIndexByte(enc, '_')+1:]
}

// IsHardware reports whether enc is a hardware encoder.
func IsHardware(enc string) bool { return hardwareKind(enc) != "" }

// hwFrames maps the hardware encoders that only take frames already on the
// device to FFmpeg's name for the device type.
var hwFrames = map[string]string{"vaapi": "vaapi", "vulkan": "vulkan", "d3d12va": "d3d12va"}

// levelIndex is 0 for "high" or "fast", 2 for "low" or "slow", and 1 for
// anything else.
func levelIndex(l string) int {
	switch l {
	case "high", "fast":
		return 0
	case "low", "slow":
		return 2
	}
	return 1
}

func level(l string, first, middle, last string) string {
	return [3]string{first, middle, last}[levelIndex(l)]
}

// speedArgs returns how enc trades speed for size, or nil when it has no
// such setting.
func speedArgs(c Context, enc, speed string) []string {
	s := func(fast, medium, slow string) string { return level(speed, fast, medium, slow) }
	switch enc {
	case "libx264", "libx265":
		return []string{"-preset", s("veryfast", "medium", "slow")}
	case "libsvtav1":
		// SVT-AV1 1.0 added presets 9 to 13.
		if c.Caps.Version.AtLeast(6, 0) {
			return []string{"-preset", s("10", "8", "5")}
		}
		return []string{"-preset", s("8", "6", "4")}
	case "libaom-av1":
		return []string{"-cpu-used", s("8", "6", "4"), "-row-mt", "1"}
	case "libvpx-vp9":
		return []string{"-deadline", "good", "-cpu-used", s("4", "2", "1"), "-row-mt", "1"}
	}
	switch hardwareKind(enc) {
	case "nvenc":
		return []string{"-preset", s("p2", "p4", "p6")}
	case "qsv":
		return []string{"-preset", s("veryfast", "medium", "veryslow")}
	case "amf":
		return []string{"-quality", s("speed", "balanced", "quality")}
	}
	return nil
}

// videoJob builds the arguments of an encode or a copy of the input's video,
// audio, and subtitles. Presets fill in what they need.
type videoJob struct {
	c       Context
	cont    container
	encoder string // a video encoder, or "copy"
	quality string // "high", "medium", or "low"
	speed   string // "fast", "medium", or "slow"
	// bitrate is the video's bits per second; 0 encodes for quality.
	bitrate int64
	filters []string // video filters, such as "scale=-2:720"
	// width, height, and fps describe the output, for bitrate estimates.
	// Zero means the same as the input.
	width, height int
	fps           float64
	audio         string // "copy", "none", or an audio encoder
	audioBitrate  string // such as "128k"; "" picks one for the encoder
	keepSubs      bool
	// pre are input options, such as -ss, and post output options, such as
	// -t, placed just before the output.
	pre, post []string
	// pass is 1 or 2 for a two-pass encode, logging to passlog.
	pass    int
	passlog string
}

var audioBitrates = map[string]string{"aac": "192k", "libopus": "128k", "libvorbis": "160k", "libmp3lame": "192k"}

func (j videoJob) build(output string) (args, notes []string, err error) {
	c := j.c
	vids := videoStreams(c)
	if len(vids) == 0 {
		return nil, nil, fmt.Errorf("the file has no video")
	}
	if j.encoder == "" {
		return nil, nil, fmt.Errorf("this FFmpeg has no encoder for that codec")
	}
	device := hwFrames[hardwareKind(j.encoder)]
	if device != "" {
		args = append(args, "-init_hw_device", device+"=hw", "-filter_hw_device", "hw")
	}
	args = append(args, j.pre...)
	args = append(args, "-i", c.Input)

	var maps, opts []string
	add := func(s ffmpeg.Stream) { maps = append(maps, "-map", "0:"+strconv.Itoa(s.Index)) }
	for _, s := range vids {
		add(s)
	}

	// Video.
	outCodec := ""
	if j.encoder == "copy" {
		for _, s := range vids {
			if !holds(j.cont.video, s.Codec) {
				return nil, nil, fmt.Errorf("%s video cannot go in %s without converting it", s.Codec, j.cont.name)
			}
		}
		outCodec = vids[0].Codec
		opts = append(opts, "-c:v", "copy")
	} else {
		for _, e := range c.Caps.Encoders {
			if e.Name == j.encoder {
				outCodec = e.Codec
			}
		}
		opts = append(opts, "-c:v", j.encoder)
		opts = append(opts, j.rateArgs(outCodec)...)
		opts = append(opts, speedArgs(c, j.encoder, j.speed)...)
		if hardwareKind(j.encoder) == "mf" {
			opts = append(opts, "-hw_encoding", "1")
		}
		filters := slices.Clone(j.filters)
		if device != "" {
			filters = append(filters, "format=nv12", "hwupload")
		}
		if len(filters) > 0 {
			opts = append(opts, "-vf", strings.Join(filters, ","))
		}
		// libx264 keeps 4:4:4 and 10-bit input as it is, which many
		// players cannot show.
		if j.encoder == "libx264" {
			opts = append(opts, "-pix_fmt", "yuv420p")
		}
	}
	// Apple devices only play H.265 in MP4 and MOV with this tag.
	if outCodec == "hevc" && j.cont.faststart {
		opts = append(opts, "-tag:v", "hvc1")
	}
	if j.pass != 0 {
		// Both passes must see the same frames. Without this, FFmpeg 5.1
		// keeps the input's timing for pass 1's null output but makes it
		// constant for MP4, adding a frame that pass 1 has no stats for.
		if c.Caps.Version.AtLeast(5, 1) {
			opts = append(opts, "-fps_mode", "passthrough")
		} else {
			opts = append(opts, "-vsync", "passthrough")
		}
	}
	if j.pass == 1 {
		// The first pass only analyzes the video.
		args = append(args, maps...)
		args = append(args, opts...)
		args = append(args, "-pass", "1", "-passlogfile", j.passlog, "-an", "-sn")
		args = append(args, j.post...)
		return append(args, "-f", "null", "-"), nil, nil
	}
	if j.pass == 2 {
		opts = append(opts, "-pass", "2", "-passlogfile", j.passlog)
	}

	// Audio.
	if auds := audioStreams(c); j.audio != "none" && len(auds) > 0 {
		enc := j.audio
		if enc == "copy" {
			for _, s := range auds {
				if !holds(j.cont.audio, s.Codec) {
					if enc, err = firstEncoder(c, j.cont.defaultAudio); err != nil {
						return nil, nil, err
					}
					notes = append(notes, fmt.Sprintf("%s audio cannot go in %s as it is, so the audio is converted with %s.", s.Codec, j.cont.name, enc))
					break
				}
			}
		}
		for _, s := range auds {
			add(s)
		}
		opts = append(opts, "-c:a", enc)
		if enc != "copy" {
			b := j.audioBitrate
			if b == "" {
				b = audioBitrates[enc]
			}
			if b != "" {
				opts = append(opts, "-b:a", b)
			}
		}
	}

	// Subtitles, and the fonts some of them need.
	if j.keepSubs {
		kept, dropped := 0, 0
		for _, s := range c.Media.Streams {
			if s.Type != "subtitle" {
				continue
			}
			if j.cont.subs == "copy" || j.cont.subs != "" && slices.Contains(textSubtitles, s.Codec) {
				add(s)
				kept++
			} else {
				dropped++
			}
		}
		if kept > 0 {
			opts = append(opts, "-c:s", j.cont.subs)
		}
		if dropped > 0 {
			notes = append(notes, fmt.Sprintf("%d picture-based subtitle tracks cannot go in %s, so they are left out.", dropped, j.cont.name))
		}
		if j.cont.attachments {
			attached := false
			for _, s := range c.Media.Streams {
				if s.Type == "attachment" {
					add(s)
					attached = true
				}
			}
			if attached {
				opts = append(opts, "-c:t", "copy")
			}
		}
	}

	if j.cont.faststart {
		opts = append(opts, "-movflags", "+faststart")
	}
	args = append(args, maps...)
	args = append(args, opts...)
	args = append(args, j.post...)
	return append(args, output), notes, nil
}

func firstEncoder(c Context, names []string) (string, error) {
	for _, n := range names {
		if c.Caps.HasEncoder(n) {
			return n, nil
		}
	}
	return "", fmt.Errorf("this FFmpeg has none of these audio encoders: %s", strings.Join(names, ", "))
}

// rateArgs returns how the video encoder spends bits: a bitrate when the
// job has one, else a quality level in the encoder's own scale.
func (j videoJob) rateArgs(codec string) []string {
	if j.bitrate > 0 {
		b := strconv.FormatInt(j.bitrate, 10)
		return []string{"-b:v", b, "-maxrate", b, "-bufsize", strconv.FormatInt(2*j.bitrate, 10)}
	}
	q := func(high, medium, low string) string { return level(j.quality, high, medium, low) }
	switch j.encoder {
	case "libx264":
		return []string{"-crf", q("18", "23", "28")}
	case "libx265":
		return []string{"-crf", q("20", "26", "30")}
	case "libvpx-vp9":
		return []string{"-crf", q("28", "33", "40"), "-b:v", "0"}
	case "libaom-av1":
		return []string{"-crf", q("24", "32", "40"), "-b:v", "0"}
	case "libsvtav1":
		if j.c.Caps.Version.AtLeast(5, 1) {
			return []string{"-crf", q("27", "35", "45")}
		}
		return []string{"-rc", "0", "-qp", q("27", "35", "45")}
	}
	switch hardwareKind(j.encoder) {
	case "nvenc":
		return []string{"-rc", "vbr", "-cq", q("19", "25", "31"), "-b:v", "0"}
	case "qsv":
		return []string{"-global_quality", q("20", "25", "30")}
	case "vaapi":
		if codec == "h264" || codec == "hevc" {
			return []string{"-qp", q("20", "25", "30")}
		}
	}
	// Encoders without a quality mode get a bitrate for the picture size.
	return []string{"-b:v", strconv.FormatInt(j.estimate(codec), 10)}
}

// estimate returns a bitrate that gives about the quality level for the
// output's size and frame rate.
func (j videoJob) estimate(codec string) int64 {
	w, h, fps := 1920, 1080, 30.0
	if vids := videoStreams(j.c); len(vids) > 0 {
		if vids[0].Width > 0 && vids[0].Height > 0 {
			w, h = vids[0].Width, vids[0].Height
		}
		if vids[0].FrameRate > 0 {
			fps = vids[0].FrameRate
		}
	}
	if j.width > 0 && j.height > 0 {
		w, h = j.width, j.height
	}
	if j.fps > 0 {
		fps = j.fps
	}
	bpp := map[string]float64{"high": 0.12, "medium": 0.08, "low": 0.05}[j.quality]
	if bpp == 0 {
		bpp = 0.08
	}
	if codec != "h264" {
		bpp *= 0.6 // newer codecs need fewer bits for the same picture
	}
	return int64(math.Round(float64(w*h)*fps*bpp/1000) * 1000)
}
