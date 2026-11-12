package ffmpeg

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Media is what ffprobe reports about a file.
type Media struct {
	Path string
	// Format is FFmpeg's name for the container, such as "matroska,webm",
	// and FormatLong is its description, such as "Matroska / WebM".
	Format     string
	FormatLong string
	Duration   time.Duration // 0 when unknown
	Size       int64         // bytes; 0 when unknown
	BitRate    int64         // bits per second; 0 when unknown
	Title      string
	Streams    []Stream
}

// Stream is one video, audio, subtitle, data, or attachment stream.
type Stream struct {
	Index int
	// Type is "video", "audio", "subtitle", "data", or "attachment".
	Type      string
	Codec     string // such as "h264"
	CodecLong string
	Profile   string
	BitRate   int64

	// Video
	Width, Height int
	FrameRate     float64 // frames per second; 0 when unknown
	PixelFormat   string
	// CoverArt is set for a still image attached to an audio file.
	CoverArt bool

	// Audio
	SampleRate    int
	Channels      int
	ChannelLayout string

	// Language is the ISO 639 code, such as "eng". FFmpeg's "und"
	// (undetermined) is reported as "".
	Language string
	Title    string
	Default  bool
	Forced   bool
}

// Probe runs ffprobe on path.
func (t *Tools) Probe(ctx context.Context, path string) (*Media, error) {
	// -i keeps a path that starts with "-" from being read as an option.
	out, err := t.Runner.Output(ctx, t.FFprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", "-i", path)
	if err != nil {
		return nil, err
	}
	var raw probeJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("reading ffprobe output for %s: %w", path, err)
	}
	m := &Media{
		Path:       path,
		Format:     raw.Format.FormatName,
		FormatLong: raw.Format.FormatLongName,
		Duration:   seconds(raw.Format.Duration),
		Size:       integer(raw.Format.Size),
		BitRate:    integer(raw.Format.BitRate),
		Title:      tag(raw.Format.Tags, "title"),
	}
	for _, s := range raw.Streams {
		st := Stream{
			Index:         s.Index,
			Type:          s.CodecType,
			Codec:         s.CodecName,
			CodecLong:     s.CodecLongName,
			Profile:       s.Profile,
			BitRate:       integer(s.BitRate),
			Width:         s.Width,
			Height:        s.Height,
			PixelFormat:   s.PixFmt,
			CoverArt:      s.Disposition["attached_pic"] == 1,
			SampleRate:    int(integer(s.SampleRate)),
			Channels:      s.Channels,
			ChannelLayout: s.ChannelLayout,
			Language:      tag(s.Tags, "language"),
			Title:         tag(s.Tags, "title"),
			Default:       s.Disposition["default"] == 1,
			Forced:        s.Disposition["forced"] == 1,
		}
		if st.Language == "und" {
			st.Language = ""
		}
		if st.Type == "video" {
			// avg_frame_rate is 0/0 for cover art. r_frame_rate is not used:
			// it is a time base guess, such as twice the real rate for some
			// raw streams.
			st.FrameRate = ratio(s.AvgFrameRate)
		}
		m.Streams = append(m.Streams, st)
	}
	return m, nil
}

type probeJSON struct {
	Format struct {
		FormatName     string            `json:"format_name"`
		FormatLongName string            `json:"format_long_name"`
		Duration       string            `json:"duration"`
		Size           string            `json:"size"`
		BitRate        string            `json:"bit_rate"`
		Tags           map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		Index         int               `json:"index"`
		CodecType     string            `json:"codec_type"`
		CodecName     string            `json:"codec_name"`
		CodecLongName string            `json:"codec_long_name"`
		Profile       string            `json:"profile"`
		BitRate       string            `json:"bit_rate"`
		Width         int               `json:"width"`
		Height        int               `json:"height"`
		PixFmt        string            `json:"pix_fmt"`
		AvgFrameRate  string            `json:"avg_frame_rate"`
		SampleRate    string            `json:"sample_rate"`
		Channels      int               `json:"channels"`
		ChannelLayout string            `json:"channel_layout"`
		Disposition   map[string]int    `json:"disposition"`
		Tags          map[string]string `json:"tags"`
	} `json:"streams"`
}

// tag looks a tag up case-insensitively: files tagged by other programs
// keep their case, such as FLAC's "TITLE".
func tag(tags map[string]string, name string) string {
	for k, v := range tags {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func seconds(s string) time.Duration {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) {
		return 0
	}
	return time.Duration(math.Round(f * float64(time.Second)))
}

func integer(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// ratio parses a rate such as "30000/1001". It returns 0 for "0/0".
func ratio(s string) float64 {
	num, den, _ := strings.Cut(s, "/")
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}
