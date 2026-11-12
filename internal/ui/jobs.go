package ui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/part-avocado/fftui/internal/cmdline"
	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/job"
	"github.com/part-avocado/fftui/internal/ui/theme"
)

// JobsChanged tells the app to redraw the jobs from the queue. Send it to
// the program whenever job.Queue.Changed fires.
type JobsChanged struct{}

// jobsScreen lists the jobs, shows the selected one's progress and log, and
// cancels jobs.
type jobsScreen struct {
	queue    *job.Queue
	jobs     []job.Job // snapshot from the queue
	selected int       // job ID
	now      func() time.Time
	width    int
	height   int

	// command is the FFmpeg command from the command line, started once
	// FFmpeg is found.
	command []string
	ff      ffmpegMsg

	confirm bool // asking whether to cancel the selected job
	logView bool // showing the selected job's whole log
	// logOffset is the first log line shown, or -1 to follow the end.
	logOffset int

	keys jobsKeys
}

type jobsKeys struct {
	Up, Down, Cancel, Retry, Copy, Log, Yes, No, Scroll, PageUp, PageDown, Top, Bottom, Back key.Binding
}

func newJobs(q *job.Queue, command []string, now func() time.Time) jobsScreen {
	return jobsScreen{queue: q, command: command, now: now, logOffset: -1, keys: jobsKeys{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("up/down", "select")),
		Down:     key.NewBinding(key.WithKeys("down", "j")),
		Cancel:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "cancel job")),
		Retry:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "retry in software")),
		Copy:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy command")),
		Log:      key.NewBinding(key.WithKeys("l", "enter"), key.WithHelp("l", "full log")),
		Yes:      key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "cancel it")),
		No:       key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n", "keep running")),
		Scroll:   key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("up/down", "scroll")),
		PageUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup/pgdn", "page")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
		Top:      key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("home/end", "start/end")),
		Bottom:   key.NewBinding(key.WithKeys("end", "G")),
		Back:     key.NewBinding(key.WithKeys("esc", "l"), key.WithHelp("esc", "back")),
	}}
}

func (s jobsScreen) Title() string { return "Jobs" }

func (s jobsScreen) current() (job.Job, bool) {
	i := slices.IndexFunc(s.jobs, func(j job.Job) bool { return j.ID == s.selected })
	if i < 0 {
		return job.Job{}, false
	}
	return s.jobs[i], true
}

func (s jobsScreen) Keys() []key.Binding {
	j, ok := s.current()
	switch {
	case !ok:
		return nil
	case s.confirm:
		return []key.Binding{s.keys.Yes, s.keys.No}
	case s.logView:
		return []key.Binding{s.keys.Back, s.keys.Scroll, s.keys.PageUp, s.keys.Top}
	}
	var keys []key.Binding
	if len(s.jobs) > 1 {
		keys = append(keys, s.keys.Up)
	}
	if !j.State.Finished() && !j.Stopping {
		keys = append(keys, s.keys.Cancel)
	}
	if j.State == job.Failed && j.Retry != nil {
		keys = append(keys, s.keys.Retry)
	}
	return append(keys, s.keys.Log, s.keys.Copy)
}

func (s jobsScreen) refresh() jobsScreen {
	s.jobs = s.queue.Jobs()
	if _, ok := s.current(); !ok && len(s.jobs) > 0 {
		s.selected = s.jobs[0].ID
	}
	// A job can finish while the question is open.
	if j, _ := s.current(); j.State.Finished() || j.Stopping {
		s.confirm = false
	}
	return s
}

func (s jobsScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case resizeMsg:
		s.width, s.height = msg.width, msg.height
	case JobsChanged:
		s = s.refresh()
	case ffmpegMsg:
		s.ff = msg
		if msg.tools != nil && s.command != nil {
			command := s.command
			s.command = nil
			return s, commandJob(msg.tools, command)
		}
	case queueJobsMsg:
		// Show the first job of a batch; the rest wait below it.
		for i, spec := range msg.specs {
			if id := s.queue.Add(spec); i == 0 {
				s.selected = id
			}
		}
		s.logView, s.confirm = false, false
		s = s.refresh()
	case tea.KeyPressMsg:
		if j, ok := s.current(); ok && !s.logView && !s.confirm && key.Matches(msg, s.keys.Copy) {
			return s, copyCmd("the command", shellCommand(j.Steps))
		}
		return s.key(msg), nil
	}
	return s, nil
}

func (s jobsScreen) key(msg tea.KeyPressMsg) jobsScreen {
	j, ok := s.current()
	if !ok {
		return s
	}
	if s.confirm {
		s.confirm = false
		switch {
		case key.Matches(msg, s.keys.Yes):
			s.queue.Cancel(j.ID)
			return s.refresh()
		case key.Matches(msg, s.keys.No):
			return s
		}
		// Any other key closes the question and does what it normally does.
	}
	if s.logView {
		page := max(1, s.logHeight())
		offset := s.logOffset
		if offset < 0 {
			offset = s.maxLogOffset(j)
		}
		switch {
		case key.Matches(msg, s.keys.Back):
			s.logView = false
			return s
		case key.Matches(msg, s.keys.Up):
			offset--
		case key.Matches(msg, s.keys.Down):
			offset++
		case key.Matches(msg, s.keys.PageUp):
			offset -= page
		case key.Matches(msg, s.keys.PageDown):
			offset += page
		case key.Matches(msg, s.keys.Top):
			offset = 0
		case key.Matches(msg, s.keys.Bottom):
			offset = s.maxLogOffset(j)
		}
		s.logOffset = max(0, offset)
		// At the end, keep following new lines.
		if s.logOffset >= s.maxLogOffset(j) {
			s.logOffset = -1
		}
		return s
	}
	i := slices.IndexFunc(s.jobs, func(x job.Job) bool { return x.ID == j.ID })
	switch {
	case key.Matches(msg, s.keys.Up) && i > 0:
		s.selected = s.jobs[i-1].ID
	case key.Matches(msg, s.keys.Down) && i < len(s.jobs)-1:
		s.selected = s.jobs[i+1].ID
	case key.Matches(msg, s.keys.Cancel) && !j.State.Finished() && !j.Stopping:
		s.confirm = true
	case key.Matches(msg, s.keys.Retry) && j.State == job.Failed && j.Retry != nil:
		s.selected = s.queue.Add(*j.Retry)
		return s.refresh()
	case key.Matches(msg, s.keys.Log):
		s.logView, s.logOffset = true, -1
	}
	return s
}

// commandJob turns FFmpeg arguments from the command line into a job,
// probing the first input for the percentage.
func commandJob(tools *ffmpeg.Tools, args []string) tea.Cmd {
	return func() tea.Msg {
		spec := job.Spec{Name: args[len(args)-1], Program: tools.FFmpeg, Steps: [][]string{args}}
		if input, ok := durationInput(args); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// If ffprobe cannot read it, FFmpeg will report why.
			if m, err := tools.Probe(ctx, input); err == nil {
				spec.Duration = m.Duration
			}
		}
		return queueJobsMsg{[]job.Spec{spec}}
	}
}

// durationInput returns the input whose duration is the output's. Options
// that cut or repeat the input make it unknown.
func durationInput(args []string) (string, bool) {
	input := ""
	for i, a := range args {
		switch {
		case a == "-t" || a == "-to" || a == "-ss" || a == "-sseof" || a == "-stream_loop" || a == "-shortest" ||
			a == "-vframes" || a == "-aframes" || strings.HasPrefix(a, "-frames"):
			return "", false
		case a == "-i" && i+1 < len(args) && input == "":
			input = args[i+1]
		}
	}
	return input, input != ""
}

func (s jobsScreen) View(t theme.Theme) string {
	j, ok := s.current()
	if !ok {
		return strings.Join(fit(s.empty(t), s.width), "\n")
	}
	if s.logView {
		return strings.Join(s.logLines(t, j), "\n")
	}
	list := s.listLines(t)
	details := s.details(t, j, s.height-len(list)-1)
	// The selected job's log fills the rest.
	room := s.height - len(list) - 1 - len(details) - 2
	if room > 0 && len(j.Log) > 0 {
		tail := fit(j.Log, s.width)
		tail = tail[max(0, len(tail)-room):]
		details = append(details, "", t.Muted.Render("Log")+t.Muted.Render(" (l for all of it)"))
		details = append(details, tail...)
	}
	return strings.Join(list, "\n") + "\n\n" + strings.Join(details, "\n")
}

func (s jobsScreen) empty(t theme.Theme) []string {
	switch {
	case s.command != nil && s.ff.err != nil:
		return []string{t.Heading.Render("Waiting for FFmpeg"), "", "Your command runs once FFmpeg is found. The Files screen explains how to set it up."}
	case s.command != nil:
		return []string{"Starting " + commandLine(s.command) + t.Ellipsis}
	}
	return []string{
		t.Heading.Render("No jobs yet"),
		"",
		"Run an FFmpeg command here by giving its arguments after --:",
		"",
		"  fftui -- -i input.mp4 -c:v libx264 output.mkv",
		"",
		t.Muted.Render("Or open files (fftui *.mp4) and press enter on one to choose a task."),
	}
}

func (s jobsScreen) listLines(t theme.Theme) []string {
	h := min(len(s.jobs), max(1, s.height/3))
	i := slices.IndexFunc(s.jobs, func(j job.Job) bool { return j.ID == s.selected })
	// Keep the selected job near the middle, so the jobs queued after a
	// new batch's first job show below it.
	start := max(0, min(i-h/2, len(s.jobs)-h))
	var lines []string
	for _, j := range s.jobs[start:min(start+h, len(s.jobs))] {
		marker, name := "  ", j.Name
		if j.ID == s.selected {
			marker, name = "> ", t.Selected.Render(j.Name)
		}
		line := marker + stateStyle(t, j.State).Render(fmt.Sprintf("%-9s", j.State)) + " " + name
		if status := s.status(t, j); status != "" {
			line += "  " + t.Muted.Render(status)
		}
		if s.width > 0 {
			line = ansi.Truncate(line, s.width, t.Ellipsis)
		}
		lines = append(lines, line)
	}
	return lines
}

// status is the short note after a job in the list.
func (s jobsScreen) status(t theme.Theme, j job.Job) string {
	switch {
	case j.Stopping && j.State == job.Running:
		return "stopping" + t.Ellipsis
	case j.State == job.Running && j.Spec.Duration > 0:
		return passNote(j) + fmt.Sprintf("%d%%", int(fraction(j)*100))
	case j.State == job.Running:
		return formatClock(j.Progress.OutTime)
	case j.State == job.Done:
		return "took " + formatClock(j.Finished.Sub(j.Started))
	case j.State == job.Failed:
		return firstLine(j.Err.Error())
	}
	return ""
}

// details describes job j in up to height lines, cutting its command
// first, since the progress under it matters more.
func (s jobsScreen) details(t theme.Theme, j job.Job, height int) []string {
	sep := t.HelpSep
	var command []string
	for _, step := range j.Steps {
		command = append(command, t.Muted.Render(commandLine(step)))
	}
	var lines []string
	if s.confirm {
		lines = append(lines, "", t.Error.Render("Cancel this job? FFmpeg stops and closes the file written so far."))
	}
	written := formatClock(j.Progress.OutTime) + " written"
	if j.Spec.Duration > 0 {
		written = formatClock(j.Progress.OutTime) + " of " + formatClock(j.Spec.Duration)
	}
	size := ""
	if j.Progress.TotalSize > 0 {
		size = sep + formatSize(j.Progress.TotalSize)
	}
	switch j.State {
	case job.Queued:
		lines = append(lines, "", "Waiting for a running job to finish (the jobs setting allows "+strconv.Itoa(s.queue.Limit())+" at once).")
	case job.Running:
		lines = append(lines, "")
		if j.Spec.Duration > 0 {
			lines = append(lines, bar(t, fraction(j), s.width))
		}
		elapsed := s.now().Sub(j.Started)
		stats := written
		if j.Progress.Speed > 0 {
			stats += sep + trimFloat(j.Progress.Speed) + "x"
		}
		stats += size + sep + formatClock(elapsed) + " elapsed"
		if f := fraction(j); j.Spec.Duration > 0 && f > 0 && f < 1 {
			left := time.Duration(float64(elapsed) * (1 - f) / f)
			stats += sep + "about " + formatClock(left) + " left"
		}
		if j.Stopping {
			stats = "Stopping" + t.Ellipsis + sep + stats
		}
		if note := passNote(j); note != "" {
			stats = strings.TrimSuffix(note, " ") + sep + stats
		}
		lines = append(lines, stats)
	case job.Done:
		lines = append(lines, "", "Done in "+formatClock(j.Finished.Sub(j.Started))+sep+formatClock(j.Progress.OutTime)+" written"+size)
	case job.Failed:
		lines = append(lines, "", t.Error.Render("Failed: ")+j.Err.Error())
		if j.Retry != nil {
			lines = append(lines, "Hardware encoders need a working device and driver. Press s to run it again with "+encoderOf(*j.Retry)+".")
		}
	case job.Cancelled:
		note := "Cancelled before it started."
		if !j.Started.IsZero() {
			note = "Cancelled after " + formatClock(j.Finished.Sub(j.Started)) + sep + written + size
		}
		lines = append(lines, "", note)
	}
	lines = fit(lines, s.width)
	command = fit(command, s.width)
	if room := max(1, height-1-len(lines)); len(command) > room {
		command = command[:room]
		command[room-1] = t.Muted.Render(t.Ellipsis)
	}
	return append(append(fit([]string{t.Heading.Render(j.Name)}, s.width), command...), lines...)
}

// logHeight is how many log lines fit under the log view's heading.
func (s jobsScreen) logHeight() int { return max(1, s.height-2) }

func (s jobsScreen) maxLogOffset(j job.Job) int {
	return max(0, len(fit(j.Log, s.width))-s.logHeight())
}

func (s jobsScreen) logLines(t theme.Theme, j job.Job) []string {
	heading := t.Heading.Render("Log of "+j.Name) + t.Muted.Render(fmt.Sprintf(" (%d lines)", j.Dropped+len(j.Log)))
	if j.Dropped > 0 {
		heading += t.Muted.Render(fmt.Sprintf(", the first %d no longer kept", j.Dropped))
	}
	lines := fit(j.Log, s.width)
	offset := s.logOffset
	if offset < 0 || offset > s.maxLogOffset(j) {
		offset = s.maxLogOffset(j)
	}
	out := fit([]string{heading}, s.width)[:1]
	out = append(out, "")
	if len(lines) == 0 {
		return append(out, t.Muted.Render("FFmpeg has not written anything yet."))
	}
	return append(out, lines[offset:min(offset+s.logHeight(), len(lines))]...)
}

// fraction is how much of a job is done, from 0 to 1. Each step of a
// multi-step job counts the same.
func fraction(j job.Job) float64 {
	if j.State == job.Done {
		return 1
	}
	step := 0.0
	switch {
	case j.Progress.End:
		step = 1
	case j.Spec.Duration > 0:
		step = min(1, max(0, float64(j.Progress.OutTime)/float64(j.Spec.Duration)))
	}
	n := max(1, len(j.Steps))
	return (float64(j.Step) + step) / float64(n)
}

// passNote is "pass 1 of 2 " for a running multi-step job, else "".
func passNote(j job.Job) string {
	if len(j.Steps) < 2 || j.State != job.Running {
		return ""
	}
	return fmt.Sprintf("pass %d of %d ", j.Step+1, len(j.Steps))
}

// encoderOf returns the video encoder of a job's last step.
func encoderOf(s job.Spec) string {
	last := s.Steps[len(s.Steps)-1]
	if i := slices.Index(last, "-c:v"); i >= 0 && i+1 < len(last) {
		return last[i+1]
	}
	return "the software encoder"
}

// bar draws a progress bar with its percentage, width cells wide.
func bar(t theme.Theme, f float64, width int) string {
	pct := fmt.Sprintf(" %3d%%", int(f*100))
	cells := max(10, width-len(pct))
	filled := int(f * float64(cells))
	return t.Selected.Render(strings.Repeat(t.BarFull, filled)) + t.Muted.Render(strings.Repeat(t.BarEmpty, cells-filled)) + pct
}

func stateStyle(t theme.Theme, s job.State) lipgloss.Style {
	switch s {
	case job.Running:
		return t.Selected
	case job.Failed:
		return t.Error
	}
	return t.Muted
}

// commandLine writes FFmpeg's arguments as one line that the user's shell
// would run, as the copy key copies it.
func commandLine(args []string) string {
	return cmdline.Join("ffmpeg", args, cmdline.Windows)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// formatClock writes d as H:MM:SS.
func formatClock(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	return fmt.Sprintf("%d:%02d:%02d", d/time.Hour, d%time.Hour/time.Minute, d%time.Minute/time.Second)
}
