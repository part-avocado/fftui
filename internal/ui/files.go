package ui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/preset"
	"github.com/part-avocado/fftui/internal/ui/theme"
)

// files lists the media files fftui works on and shows what ffprobe reports
// about the selected one. When FFmpeg is missing, it explains how to install
// it instead. Files come from the command line until the file picker arrives.
type files struct {
	entries    []fileEntry
	selected   int
	offset     int // first line of the media info shown
	width      int
	height     int
	ff         ffmpegMsg
	gen        int // counts FFmpeg searches, so results from an older one are dropped
	install    []string
	configPath string
	outputDir  string
	keys       filesKeys

	// menu and form are open while choosing a task for the selected or
	// marked files and filling in its form; raw while writing a custom
	// command.
	menu *presetMenu
	form *form
	raw  *rawForm
}

type fileEntry struct {
	path string
	// media and err are both nil while ffprobe has not answered yet.
	media *ffmpeg.Media
	err   error
	// marked files get the same task together.
	marked bool
}

type probeMsg struct {
	gen, index int
	media      *ffmpeg.Media
	err        error
}

type filesKeys struct {
	Up, Down, PageUp, PageDown, Retry, Task, Mark, MarkAll, Choose, Back key.Binding
}

func newFiles(paths, install []string, configPath, outputDir string) files {
	f := files{install: install, configPath: configPath, outputDir: outputDir, keys: filesKeys{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("up/down", "select")),
		Down:     key.NewBinding(key.WithKeys("down", "j")),
		PageUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup/pgdn", "scroll")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
		Retry:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "look again")),
		Task:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "choose a task")),
		Mark:     key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "mark for batch")),
		MarkAll:  key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "mark all")),
		Choose:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}}
	for _, p := range paths {
		f.entries = append(f.entries, fileEntry{path: p})
	}
	return f
}

func (f files) Title() string { return "Files" }

// captures reports whether the screen takes every key, as a form does while
// the user types.
func (f files) captures() bool { return f.menu != nil || f.form != nil || f.raw != nil }

// readable reports whether ffprobe has read entry i, so a task can use it.
func (f files) readable(i int) bool {
	return f.ff.tools != nil && f.entries[i].media != nil
}

// marked returns the indexes of the marked files ffprobe could read.
func (f files) marked() []int {
	var out []int
	for i, e := range f.entries {
		if e.marked && f.readable(i) {
			out = append(out, i)
		}
	}
	return out
}

// media returns the selected file's ffprobe report, or nil.
func (f files) media() *ffmpeg.Media {
	if len(f.entries) == 0 || f.ff.tools == nil {
		return nil
	}
	return f.entries[f.selected].media
}

func (f files) Keys() []key.Binding {
	switch {
	case f.raw != nil:
		return f.raw.keyList()
	case f.form != nil:
		return f.form.keyList()
	case f.menu != nil:
		return []key.Binding{f.keys.Up, f.keys.Choose, f.keys.Back}
	}
	var keys []key.Binding
	if n := len(f.marked()); n > 0 {
		f.keys.Task.SetHelp("enter", fmt.Sprintf("task for %d marked", n))
		keys = append(keys, f.keys.Task)
	} else if f.media() != nil {
		keys = append(keys, f.keys.Task)
	}
	if f.media() != nil && len(f.entries) > 1 {
		keys = append(keys, f.keys.Mark, f.keys.MarkAll)
	}
	if f.ff.err != nil {
		keys = append(keys, f.keys.Retry)
	}
	if len(f.entries) > 1 && f.ff.tools != nil {
		keys = append(keys, f.keys.Up)
	}
	// Styles do not change how many lines the info takes, so the zero theme
	// is enough to count them.
	if len(f.info(theme.Theme{})) > f.infoHeight() {
		keys = append(keys, f.keys.PageUp)
	}
	return keys
}

func (f files) Update(msg tea.Msg) (Screen, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case resizeMsg:
		f.width, f.height = msg.width, msg.height
	case ffmpegMsg:
		f.ff = msg
		f.gen++
		f.menu, f.form, f.raw = nil, nil, nil
		for i := range f.entries {
			f.entries[i].media, f.entries[i].err = nil, nil
		}
		if msg.tools != nil && len(f.entries) > 0 {
			cmd = f.probe(0)
		}
	case probeMsg:
		if msg.gen != f.gen {
			return f, nil
		}
		f.entries[msg.index].media, f.entries[msg.index].err = msg.media, msg.err
		// Probe one file at a time, so a long list does not start hundreds
		// of ffprobe processes at once.
		if next := msg.index + 1; next < len(f.entries) {
			cmd = f.probe(next)
		}
	case settingsMsg:
		f.outputDir = msg.cfg.OutputDir
	case queueJobsMsg:
		// A batch is on its way; start the next one from scratch.
		for i := range f.entries {
			f.entries[i].marked = false
		}
	case tea.PasteMsg, tea.KeyPressMsg:
		if f.raw != nil || f.form != nil || f.menu != nil {
			return f.modalKey(msg)
		}
		k, ok := msg.(tea.KeyPressMsg)
		if !ok {
			break
		}
		switch {
		case key.Matches(k, f.keys.Task) && len(f.marked()) > 0:
			var inputs []preset.Context
			for _, i := range f.marked() {
				inputs = append(inputs, f.presetContext(i))
			}
			m := newPresetMenu(inputs)
			f.menu = &m
		case key.Matches(k, f.keys.Task) && f.media() != nil:
			m := newPresetMenu([]preset.Context{f.presetContext(f.selected)})
			f.menu = &m
		case key.Matches(k, f.keys.Mark) && f.media() != nil:
			f.entries[f.selected].marked = !f.entries[f.selected].marked
			// Marking moves on, so space marks a run of files.
			if f.selected < len(f.entries)-1 {
				f.selected++
				f.offset = 0
			}
		case key.Matches(k, f.keys.MarkAll) && f.ff.tools != nil:
			// a marks every readable file, or unmarks them all when they
			// are all marked.
			readable := 0
			for i := range f.entries {
				if f.readable(i) {
					readable++
				}
			}
			all := len(f.marked()) == readable
			for i := range f.entries {
				f.entries[i].marked = !all && f.readable(i)
			}
		case key.Matches(k, f.keys.Up) && f.selected > 0:
			f.selected--
			f.offset = 0
		case key.Matches(k, f.keys.Down) && f.selected < len(f.entries)-1:
			f.selected++
			f.offset = 0
		case key.Matches(k, f.keys.PageUp):
			f.offset -= f.infoHeight()
		case key.Matches(k, f.keys.PageDown):
			f.offset += f.infoHeight()
		case key.Matches(k, f.keys.Retry) && f.ff.err != nil:
			cmd = func() tea.Msg { return findFFmpegMsg{} }
		}
	}
	f.offset = max(0, min(f.offset, len(f.info(theme.Theme{}))-f.infoHeight()))
	return f, cmd
}

// presetContext describes entry i for a preset.
func (f files) presetContext(i int) preset.Context {
	e := f.entries[i]
	work := filepath.Join(os.TempDir(), fmt.Sprintf("fftui-%d-%d-%d", os.Getpid(), time.Now().UnixNano(), i))
	return preset.Context{Input: e.path, Media: e.media, Caps: f.ff.tools.Caps, Work: work}
}

// modalKey sends a key or paste to the open raw form, form, or menu. Esc
// in one goes back to the one before it.
func (f files) modalKey(msg tea.Msg) (Screen, tea.Cmd) {
	switch {
	case f.raw != nil:
		done, cmd := f.raw.update(msg)
		if done {
			f.raw = nil
			// Closing with a command means the job started: the task is
			// done. Esc goes back to the form or menu.
			if cmd != nil {
				f.form, f.menu = nil, nil
			}
		}
		return f, cmd
	case f.form != nil:
		res, cmd := f.form.update(msg)
		switch res {
		case formClose:
			f.form = nil
		case formRaw:
			j := f.form.jobs[0]
			f.raw = newRawForm(j.c, f.ff.tools.FFmpeg, j.plan.Steps[0])
		}
		if cmd != nil && res == formClose {
			// Jobs started: the task is done.
			f.menu = nil
		}
		return f, cmd
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return f, nil
	}
	m := *f.menu
	n := len(m.problems)
	switch {
	case key.Matches(k, f.keys.Back):
		f.menu = nil
		return f, nil
	case key.Matches(k, f.keys.Up):
		m.selected = (m.selected + n - 1) % n
	case key.Matches(k, f.keys.Down):
		m.selected = (m.selected + 1) % n
	case key.Matches(k, f.keys.Choose) && m.problems[m.selected] == nil && m.custom():
		c := m.inputs[0]
		f.raw = newRawForm(c, f.ff.tools.FFmpeg, customArgs(c, f.outputDir))
	case key.Matches(k, f.keys.Choose) && m.problems[m.selected] == nil:
		f.form = newForm(m.presets[m.selected], m.inputs, f.ff.tools.FFmpeg, f.outputDir)
	}
	f.menu = &m
	return f, nil
}

func (f files) probe(i int) tea.Cmd {
	tools, gen, path := f.ff.tools, f.gen, f.entries[i].path
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m, err := tools.Probe(ctx, path)
		return probeMsg{gen: gen, index: i, media: m, err: err}
	}
}

func (f files) listHeight() int { return min(len(f.entries), max(1, f.height/3)) }

// infoHeight is the number of lines below the file list and its blank line.
func (f files) infoHeight() int { return max(1, f.height-f.listHeight()-1) }

func (f files) View(t theme.Theme) string {
	switch {
	case f.raw != nil:
		return f.raw.view(t, f.width, f.height)
	case f.form != nil:
		return f.form.view(t, f.width, f.height)
	case f.menu != nil:
		return f.menu.view(t, f.width, f.height)
	case f.ff.err != nil:
		return strings.Join(fit(f.missing(t), f.width), "\n")
	case f.ff.tools == nil:
		return "Looking for FFmpeg" + t.Ellipsis
	case len(f.entries) == 0:
		return strings.Join(fit([]string{
			t.Heading.Render("No files yet"),
			"",
			"Start fftui with the files to work on:",
			"",
			`  fftui video.mp4 "other clip.mkv"`,
			"",
			t.Muted.Render("Choosing files here comes with the file picker in a later milestone."),
		}, f.width), "\n")
	}
	info := f.info(t)
	start := min(f.offset, len(info))
	end := min(start+f.infoHeight(), len(info))
	return strings.Join(f.listLines(t), "\n") + "\n\n" + strings.Join(info[start:end], "\n")
}

func (f files) listLines(t theme.Theme) []string {
	h := f.listHeight()
	start := max(0, f.selected-h+1)
	// Once a file is marked, every line has room for the mark.
	anyMarked := slices.ContainsFunc(f.entries, func(e fileEntry) bool { return e.marked })
	var lines []string
	for i := start; i < min(start+h, len(f.entries)); i++ {
		e := f.entries[i]
		marker, name := "  ", e.path
		if i == f.selected {
			marker, name = "> ", t.Selected.Render(e.path)
		}
		switch {
		case e.marked:
			marker += t.Selected.Render(t.Check) + " "
		case anyMarked:
			marker += "  "
		}
		var status string
		switch {
		case e.err != nil:
			status = t.Error.Render("can't read")
		case e.media == nil:
			status = t.Muted.Render("reading" + t.Ellipsis)
		default:
			status = t.Muted.Render(formatDuration(e.media.Duration) + "  " + streamSummary(e.media))
		}
		line := marker + name + "  " + status
		if f.width > 0 {
			line = ansi.Truncate(line, f.width, t.Ellipsis)
		}
		lines = append(lines, line)
	}
	return lines
}

// info returns the selected file's details, wrapped to the screen's width.
func (f files) info(t theme.Theme) []string {
	if len(f.entries) == 0 || f.ff.tools == nil {
		return nil
	}
	var lines []string
	switch e := f.entries[f.selected]; {
	case e.err != nil:
		lines = []string{t.Error.Render("Could not read this file"), "", e.err.Error()}
	case e.media == nil:
		lines = []string{"Reading " + e.path + t.Ellipsis}
	default:
		lines = mediaLines(t, e.media)
	}
	return fit(lines, f.width)
}

// missing explains why FFmpeg cannot be used and how to fix it.
func (f files) missing(t theme.Theme) []string {
	var nf *ffmpeg.NotFoundError
	notFound := errors.As(f.ff.err, &nf)
	heading := "FFmpeg is not working"
	switch {
	case notFound && nf.Path != "":
		heading = "The chosen " + nf.Program + " does not work"
	case notFound:
		heading = nf.Program + " not found"
	}
	lines := []string{t.Error.Render(heading), "", f.ff.err.Error()}
	if notFound && len(nf.Searched) > 0 {
		lines = append(lines, t.Muted.Render("Looked on PATH and in "+strings.Join(nf.Searched, ", ")))
	}
	if notFound && nf.Path == "" {
		if len(f.install) > 0 {
			lines = append(lines, "", "Install FFmpeg with:")
			for _, c := range f.install {
				lines = append(lines, "  "+c)
			}
		}
		lines = append(lines, "", "Builds for every OS: "+ffmpeg.DownloadURL)
	}
	lines = append(lines,
		"",
		"Already installed somewhere else? Point fftui at it:",
		"  fftui --ffmpeg /path/to/ffmpeg",
		"  FFTUI_FFMPEG=/path/to/ffmpeg   (environment variable)",
		`  ffmpeg_path = "/path/to/ffmpeg"   in `+f.configPath,
	)
	if notFound && nf.Program == "ffprobe" {
		lines = append(lines, `  ffprobe_path = "/path/to/ffprobe"   in `+f.configPath)
	}
	return append(lines, "", "Press r to look again.")
}

func mediaLines(t theme.Theme, m *ffmpeg.Media) []string {
	field := func(name, value string) string {
		return t.Muted.Render(fmt.Sprintf("%-10s", name)) + value
	}
	container := m.FormatLong
	if m.Format != "" {
		container += " (" + m.Format + ")"
	}
	lines := []string{
		t.Heading.Render(m.Path),
		field("Container", container),
		field("Duration", formatDuration(m.Duration)),
		field("Size", formatSize(m.Size)),
		field("Bit rate", formatBitRate(m.BitRate)),
	}
	if m.Title != "" {
		lines = append(lines, field("Title", m.Title))
	}
	lines = append(lines, "", t.Heading.Render(fmt.Sprintf("Streams (%d)", len(m.Streams))))
	for _, s := range m.Streams {
		lines = append(lines, streamLine(t, s))
	}
	return lines
}

func streamLine(t theme.Theme, s ffmpeg.Stream) string {
	codec := s.Codec
	if codec == "" {
		codec = "unknown codec"
	}
	if s.Profile != "" {
		codec += " (" + s.Profile + ")"
	}
	parts := []string{codec}
	switch s.Type {
	case "video":
		if s.Width > 0 && s.Height > 0 {
			parts = append(parts, fmt.Sprintf("%dx%d", s.Width, s.Height))
		}
		if s.FrameRate > 0 {
			parts = append(parts, trimFloat(s.FrameRate)+" fps")
		}
		if s.PixelFormat != "" {
			parts = append(parts, s.PixelFormat)
		}
	case "audio":
		if s.SampleRate > 0 {
			parts = append(parts, trimFloat(float64(s.SampleRate)/1000)+" kHz")
		}
		if s.ChannelLayout != "" {
			parts = append(parts, s.ChannelLayout)
		} else if s.Channels > 0 {
			parts = append(parts, fmt.Sprintf("%d ch", s.Channels))
		}
	}
	if s.BitRate > 0 {
		parts = append(parts, formatBitRate(s.BitRate))
	}
	if s.Language != "" {
		parts = append(parts, s.Language)
	}
	if s.Title != "" {
		parts = append(parts, `"`+s.Title+`"`)
	}
	var flags []string
	if s.CoverArt {
		flags = append(flags, "cover art")
	}
	if s.Default {
		flags = append(flags, "default")
	}
	if s.Forced {
		flags = append(flags, "forced")
	}
	line := fmt.Sprintf("%2d  %-8s  %s", s.Index, s.Type, strings.Join(parts, "  "))
	if len(flags) > 0 {
		line += "  " + t.Muted.Render(strings.Join(flags, "  "))
	}
	return line
}

// streamSummary counts streams by type, such as "1 video, 2 audio".
func streamSummary(m *ffmpeg.Media) string {
	var order []string
	counts := map[string]int{}
	for _, s := range m.Streams {
		if counts[s.Type] == 0 {
			order = append(order, s.Type)
		}
		counts[s.Type]++
	}
	if len(order) == 0 {
		return "no streams"
	}
	parts := make([]string, len(order))
	for i, typ := range order {
		parts[i] = fmt.Sprintf("%d %s", counts[typ], typ)
	}
	return strings.Join(parts, ", ")
}

// fit wraps each line to width; a width of 0 leaves lines as they are.
func fit(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	var out []string
	for _, line := range lines {
		out = append(out, strings.Split(ansi.Wrap(line, width, ""), "\n")...)
	}
	return out
}

// formatDuration writes d as H:MM:SS.mmm, the way FFmpeg does.
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "unknown"
	}
	d = d.Round(time.Millisecond)
	h := d / time.Hour
	m := d % time.Hour / time.Minute
	s := d % time.Minute / time.Second
	ms := d % time.Second / time.Millisecond
	return fmt.Sprintf("%d:%02d:%02d.%03d", h, m, s, ms)
}

func formatSize(n int64) string {
	if n <= 0 {
		return "unknown"
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v, i := float64(n)/1024, 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func formatBitRate(bps int64) string {
	switch {
	case bps <= 0:
		return "unknown"
	case bps >= 1_000_000:
		return fmt.Sprintf("%.1f Mb/s", float64(bps)/1e6)
	default:
		return fmt.Sprintf("%d kb/s", (bps+500)/1000)
	}
}

// trimFloat writes f with at most two decimals and no trailing zeros, so
// 30000/1001 is "29.97" and 25 is "25".
func trimFloat(f float64) string {
	return strconv.FormatFloat(math.Round(f*100)/100, 'f', -1, 64)
}
