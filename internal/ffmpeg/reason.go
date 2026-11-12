package ffmpeg

import (
	"regexp"
	"strings"
)

// tailLines is how many of FFmpeg's last log lines Reason looks at.
const tailLines = 30

// logTag matches the "[name @ 0x1234]" that FFmpeg puts before a message from
// one of its parts, such as an encoder or filter.
var logTag = regexp.MustCompile(`^\[([^\]@]+?) @ 0x[0-9a-f]+\] *`)

// generic are messages FFmpeg adds after the real error, saying only that
// something failed.
var generic = []string{
	"Conversion failed",
	"Error while opening encoder",
	"Error initializing output stream",
	"Error opening output file",
	"Error opening input file",
	"Error splitting the argument list",
	"Error reinitializing filters",
	"Error initializing filter",
	"Error while processing the decoded data",
	"Error selecting an encoder",
	"Error sending frames to consumers",
	"Failed to inject frame into filter network",
	"Task finished with error code",
	"Terminating thread with return code",
	"Could not open encoder before EOF",
	"Nothing was written into output file",
	"Qavg:",
	"frames left in the queue on closing",
	"Press [q] to stop",
	"Exiting normally",
}

// Reason picks the line of FFmpeg's log that best says why it failed, such
// as "h264_nvenc: Cannot load libcuda.so.1". FFmpeg follows the real error
// with lines like "Conversion failed!", so the last line is rarely the
// useful one. It returns "" for an empty log.
func Reason(log []string) string {
	log = log[max(0, len(log)-tailLines):]
	reason, last := "", ""
	for i, line := range log {
		line = strings.TrimRight(line, " \r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		last = strings.TrimSpace(line)
		if noise(line) {
			continue
		}
		reason = clean(line)
		// FFmpeg 6 and later name the missing input on a later line.
		if msg, ok := strings.CutPrefix(reason, "Error opening input: "); ok {
			for _, next := range log[i+1:] {
				if name, ok := strings.CutPrefix(strings.TrimSpace(next), "Error opening input file "); ok {
					reason = strings.TrimSuffix(name, ".") + ": " + msg
					break
				}
			}
		}
	}
	if reason == "" {
		return last
	}
	return reason
}

// noise reports lines that do not explain a failure: FFmpeg's description
// of the files, its stats, and the generic messages.
func noise(line string) bool {
	if line[0] == ' ' || line[0] == '\t' {
		return true // stream and metadata details
	}
	for _, p := range []string{"Input #", "Output #", "Stream mapping:", "frame=", "size="} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	for _, g := range generic {
		if strings.Contains(line, g) {
			return true
		}
	}
	return false
}

// clean replaces FFmpeg's tags with the name of the part that wrote the
// line. Tags of FFmpeg's own plumbing, such as "out#0/mp4", are dropped.
func clean(line string) string {
	name := ""
	for {
		m := logTag.FindStringSubmatch(line)
		if m == nil {
			break
		}
		line = line[len(m[0]):]
		switch n := m[1]; {
		case strings.Contains(n, "#"), n == "AVFilterGraph":
		case strings.HasPrefix(n, "Parsed_"):
			// Parsed_scale_0 is the first scale filter.
			n = strings.TrimPrefix(n, "Parsed_")
			if i := strings.LastIndexByte(n, '_'); i > 0 {
				n = n[:i]
			}
			name = n + " filter"
		default:
			name = strings.TrimPrefix(n, "enc:")
		}
	}
	line = strings.TrimSpace(line)
	if name != "" {
		return name + ": " + line
	}
	return line
}
