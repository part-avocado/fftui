package ffmpeg

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
)

// Progress is one update from `ffmpeg -progress`. FFmpeg writes one about
// every half second. Fields FFmpeg reports as N/A are zero.
type Progress struct {
	Frame     int64
	FPS       float64
	OutTime   time.Duration // how much of the output has been written
	TotalSize int64         // output bytes so far
	Speed     float64       // multiple of real time, such as 2.5
	// End is set on the last update, which FFmpeg writes once the outputs
	// are finished.
	End bool
}

// readProgress reads `-progress` output from r and calls fn for each
// update. FFmpeg writes an update as key=value lines ending with
// progress=continue or progress=end. It keeps reading to the end of r even
// after a bad line, so FFmpeg never blocks on a full pipe.
func readProgress(r io.Reader, fn func(Progress)) {
	var p Progress
	var haveUS bool
	scanLines(r, '\n', func(line string) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			return
		}
		value = strings.TrimSpace(value)
		switch key {
		case "frame":
			p.Frame, _ = strconv.ParseInt(value, 10, 64)
		case "fps":
			p.FPS, _ = strconv.ParseFloat(value, 64)
		case "total_size":
			p.TotalSize, _ = strconv.ParseInt(value, 10, 64)
		case "out_time_us":
			if us, err := strconv.ParseInt(value, 10, 64); err == nil {
				p.OutTime, haveUS = time.Duration(us)*time.Microsecond, true
			}
		case "out_time_ms":
			// Despite its name this is also in microseconds. Use it only
			// if out_time_us is missing.
			if us, err := strconv.ParseInt(value, 10, 64); err == nil && !haveUS {
				p.OutTime = time.Duration(us) * time.Microsecond
			}
		case "speed":
			p.Speed, _ = strconv.ParseFloat(strings.TrimSuffix(value, "x"), 64)
		case "progress":
			p.End = value == "end"
			// Before the first packet, and while seeking, FFmpeg can report
			// a negative time.
			p.OutTime = max(p.OutTime, 0)
			fn(p)
			p, haveUS = Progress{}, false
		}
	})
}

// scanLines calls fn with each line of r, split at sep or, for the stderr
// log, at either '\r' or '\n' when sep is '\r'. Empty lines are skipped. It
// always reads r to the end, discarding lines too long to buffer.
func scanLines(r io.Reader, sep byte, fn func(string)) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	s.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		for i, b := range data {
			if b == '\n' || sep == '\r' && b == '\r' {
				return i + 1, data[:i], nil
			}
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	for s.Scan() {
		if line := strings.TrimRight(s.Text(), "\r"); strings.TrimSpace(line) != "" {
			fn(line)
		}
	}
	// A line longer than the buffer stops the scanner. Keep draining so the
	// process does not block writing to us.
	_, _ = io.Copy(io.Discard, r)
}
