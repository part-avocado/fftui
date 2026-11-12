package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/part-avocado/fftui/internal/cmdline"
	"github.com/part-avocado/fftui/internal/job"
	"github.com/part-avocado/fftui/internal/preset"
	"github.com/part-avocado/fftui/internal/ui/theme"
)

// queueJobsMsg asks the Jobs screen to run jobs, and the app to show them.
type queueJobsMsg struct{ specs []job.Spec }

// copyMsg asks the app to put text on the clipboard; what names it in the
// message that confirms it, such as "the command".
type copyMsg struct{ what, text string }

func copyCmd(what, text string) tea.Cmd {
	return func() tea.Msg { return copyMsg{what, text} }
}

// outputKey is where a form keeps its output path, or in a batch its output
// folder, among the values.
const outputKey = "output"

// form is one preset's form for one or more inputs: its fields, where the
// output goes, and the FFmpeg commands they make, rebuilt after every
// change. With several inputs, the fields come from the first and apply to
// every one.
type form struct {
	p       preset.Preset
	inputs  []preset.Context
	program string // the ffmpeg that runs the jobs
	dir     string // the output_dir setting

	ed     editor
	fields []preset.Field
	// outputEdited is set once the user types an output path, so it stops
	// following the other fields.
	outputEdited bool

	jobs   []plannedJob // one per input, when err is nil
	err    error        // why the form cannot start
	exists int          // how many outputs are already there
	// confirm is set while asking whether to overwrite outputs.
	confirm bool

	keys formKeys
}

// plannedJob is what the form runs for one input.
type plannedJob struct {
	c      preset.Context
	values preset.Values
	out    string
	plan   preset.Plan
	exists bool
}

type formKeys struct {
	Up, Change, Start, Copy, Raw, Back, Yes, No key.Binding
}

func newFormKeys() formKeys {
	return formKeys{
		Up:     key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("up/down", "field")),
		Change: key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("left/right", "change")),
		Start:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "start")),
		Copy:   key.NewBinding(key.WithKeys("ctrl+y"), key.WithHelp("ctrl+y", "copy command")),
		Raw:    key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "edit command")),
		Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Yes:    key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "overwrite")),
		No:     key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n", "keep it")),
	}
}

func newForm(p preset.Preset, inputs []preset.Context, program, dir string) *form {
	f := &form{p: p, inputs: inputs, program: program, dir: dir, keys: newFormKeys()}
	f.ed.values = preset.Values{}
	if f.batch() {
		f.ed.values[outputKey] = dir
	}
	f.refresh()
	f.ed.cursor = len([]rune(f.ed.value()))
	return f
}

func (f *form) batch() bool { return len(f.inputs) > 1 }

// refresh fills in the form and rebuilds its commands.
func (f *form) refresh() {
	values, fields := preset.Fill(f.p, f.inputs[0], f.ed.values)
	out := preset.Field{Key: outputKey, Label: "Output", Kind: preset.Text}
	if f.batch() {
		out.Label, out.Hint = "Output folder", "empty: next to each input"
	} else if !f.outputEdited {
		values[outputKey] = preset.DefaultOutput(f.p, f.inputs[0], values, f.dir)
	}
	f.ed.values, f.fields = values, fields
	f.ed.setRows(append(slices.Clone(fields), out))
	f.jobs, f.err = f.plan()
	f.exists = 0
	for _, j := range f.jobs {
		if j.exists {
			f.exists++
		}
	}
}

// plan builds the job for every input, or says why one cannot run.
func (f *form) plan() ([]plannedJob, error) {
	var jobs []plannedJob
	writer := map[string]string{} // output to the input that writes it
	for _, c := range f.inputs {
		name := ""
		if f.batch() {
			name = filepath.Base(c.Input) + ": "
		}
		// The first input's answers, with choices this input does not
		// offer reset to its defaults.
		v, _ := preset.Fill(f.p, c, f.ed.values)
		out := expandHome(f.ed.values[outputKey])
		if f.batch() {
			out = preset.DefaultOutput(f.p, c, v, out)
		}
		plan, err := f.p.Build(c, v, out)
		if err != nil {
			return nil, errors.New(name + err.Error())
		}
		exists, err := checkOutput(c.Input, out)
		if err != nil {
			return nil, errors.New(name + err.Error())
		}
		key := filepath.Clean(out)
		if prev, ok := writer[key]; ok {
			return nil, fmt.Errorf("%s and %s would both write %s; choose another output folder or fewer files",
				filepath.Base(prev), filepath.Base(c.Input), filepath.Base(out))
		}
		writer[key] = c.Input
		jobs = append(jobs, plannedJob{c: c, values: v, out: out, plan: plan, exists: exists})
	}
	return jobs, nil
}

// expandHome replaces a leading ~ with the home folder, as shells do.
func expandHome(path string) string {
	rest, ok := strings.CutPrefix(path, "~")
	if !ok || (rest != "" && !os.IsPathSeparator(rest[0])) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return home + rest
}

// checkOutput says why out cannot be written, and whether it exists.
func checkOutput(input, out string) (exists bool, err error) {
	if strings.TrimSpace(out) == "" {
		return false, errors.New("choose an output file")
	}
	outInfo, statErr := os.Stat(out)
	if statErr == nil {
		if inInfo, err := os.Stat(input); err == nil && os.SameFile(inInfo, outInfo) {
			return false, errors.New("the output is the input file; choose another name")
		}
		if outInfo.IsDir() {
			return false, errors.New("the output is a folder; add a file name")
		}
		return true, nil
	}
	if a, b := filepath.Clean(input), filepath.Clean(out); a == b {
		return false, errors.New("the output is the input file; choose another name")
	}
	if dir := filepath.Dir(out); dir != "" {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return false, fmt.Errorf("the folder %s does not exist", dir)
		}
	}
	return false, nil
}

// canRaw reports whether the command can be edited as text: raw mode runs
// one FFmpeg command on one input.
func (f *form) canRaw() bool {
	return f.err == nil && !f.batch() && len(f.jobs[0].plan.Steps) == 1
}

func (f *form) keyList() []key.Binding {
	if f.confirm {
		return []key.Binding{f.keys.Yes, f.keys.No}
	}
	keys := []key.Binding{f.keys.Up}
	if f.ed.focused().Kind == preset.Choice {
		keys = append(keys, f.keys.Change)
	}
	keys = append(keys, f.keys.Start)
	if f.err == nil {
		keys = append(keys, f.keys.Copy)
	}
	if f.canRaw() {
		keys = append(keys, f.keys.Raw)
	}
	return append(keys, f.keys.Back)
}

// formResult says what the form wants after a key.
type formResult int

const (
	formStay formResult = iota
	formClose
	formRaw // open the command in raw mode
)

// update handles a key or paste, and returns a command when it started jobs
// or copied the command.
func (f *form) update(msg tea.Msg) (formResult, tea.Cmd) {
	changed := ""
	switch msg := msg.(type) {
	case tea.PasteMsg:
		changed = f.ed.insert(msg.Content)
	case tea.KeyPressMsg:
		if f.confirm {
			f.confirm = false
			if key.Matches(msg, f.keys.Yes) {
				return formClose, f.start(true)
			}
			return formStay, nil
		}
		switch {
		case key.Matches(msg, f.keys.Back):
			return formClose, nil
		case key.Matches(msg, f.keys.Start):
			switch {
			case f.err != nil:
			case f.exists > 0:
				f.confirm = true
			default:
				return formClose, f.start(false)
			}
			return formStay, nil
		case key.Matches(msg, f.keys.Copy):
			if f.err != nil {
				return formStay, nil
			}
			var lines []string
			for _, j := range f.jobs {
				lines = append(lines, shellCommand(j.plan.Steps))
			}
			return formStay, copyCmd("the command", strings.Join(lines, "\n"))
		case key.Matches(msg, f.keys.Raw):
			if f.canRaw() {
				return formRaw, nil
			}
			return formStay, nil
		}
		changed = f.ed.key(msg)
	}
	if changed != "" {
		if changed == outputKey {
			f.outputEdited = true
		}
		f.refresh()
	}
	return formStay, nil
}

// shellCommand writes a job's steps as one line a shell can run; a later
// step runs only if the one before it worked.
func shellCommand(steps [][]string) string {
	var parts []string
	for _, s := range steps {
		parts = append(parts, cmdline.Join("ffmpeg", s, cmdline.Windows))
	}
	return strings.Join(parts, " && ")
}

// start returns the command that queues the jobs, overwriting outputs that
// exist when overwrite is set.
func (f *form) start(overwrite bool) tea.Cmd {
	var specs []job.Spec
	for _, j := range f.jobs {
		y := overwrite && j.exists
		spec := f.spec(j.plan, j.out, y)
		// A hardware encoder can fail for lack of a device or driver, so
		// keep the same job with the software encoder ready to retry.
		if enc := j.values[preset.EncoderKey]; preset.IsHardware(enc) {
			v, fields := preset.Fill(f.p, j.c, j.values)
			for _, field := range fields {
				if field.Key != preset.EncoderKey || preset.IsHardware(field.Options[0].Value) {
					continue
				}
				v[preset.EncoderKey] = field.Options[0].Value
				v, _ = preset.Fill(f.p, j.c, v)
				if plan, err := f.p.Build(j.c, v, j.out); err == nil {
					retry := f.spec(plan, j.out, y)
					spec.Retry = &retry
				}
			}
		}
		specs = append(specs, spec)
	}
	return func() tea.Msg { return queueJobsMsg{specs} }
}

func (f *form) spec(plan preset.Plan, out string, overwrite bool) job.Spec {
	steps := slices.Clone(plan.Steps)
	if overwrite {
		last := len(steps) - 1
		steps[last] = append([]string{"-y"}, steps[last]...)
	}
	return job.Spec{Name: filepath.Base(out), Program: f.program, Steps: steps, Duration: plan.Duration, Output: out, Temp: plan.Temp}
}

// view draws the form in width × height cells.
func (f *form) view(t theme.Theme, width, height int) string {
	what := filepath.Base(f.inputs[0].Input)
	if f.batch() {
		what = fmt.Sprintf("%d files", len(f.inputs))
	}
	header := fit([]string{
		t.Heading.Render(f.p.Name) + t.Muted.Render(" "+t.HelpSep+" "+what),
		t.Muted.Render(f.p.Description),
	}, width)

	var bottom []string
	switch {
	case f.confirm && f.batch():
		bottom = append(bottom, t.Error.Render(fmt.Sprintf("%d of %d outputs already exist. Overwrite them?", f.exists, len(f.jobs))))
	case f.confirm:
		bottom = append(bottom, t.Error.Render(filepath.Base(f.jobs[0].out)+" already exists. Overwrite it?"))
	case f.err != nil:
		bottom = append(bottom, t.Error.Render("Can't start: ")+f.err.Error())
	case f.exists > 0 && f.batch():
		verb := "exist"
		if f.exists == 1 {
			verb = "exists"
		}
		bottom = append(bottom, t.Error.Render(fmt.Sprintf("%d of the outputs already %s; enter asks before overwriting them.", f.exists, verb)))
	case f.exists > 0:
		bottom = append(bottom, t.Error.Render(filepath.Base(f.jobs[0].out)+" already exists; enter asks before overwriting it."))
	}
	var notes []string
	for _, j := range f.jobs {
		for _, n := range j.plan.Notes {
			if !slices.Contains(notes, n) {
				notes = append(notes, n)
			}
		}
	}
	for _, n := range notes {
		bottom = append(bottom, t.Muted.Render(n))
	}
	if f.err == nil {
		if len(bottom) > 0 {
			bottom = append(bottom, "")
		}
		heading := "Command"
		if f.batch() {
			heading = "Command for " + filepath.Base(f.inputs[0].Input) + t.Muted.Render(fmt.Sprintf(" (and %d more like it)", len(f.jobs)-1))
		}
		bottom = append(bottom, t.Heading.Render(heading))
		steps := f.jobs[0].plan.Steps
		for i, step := range steps {
			line := cmdline.Join("ffmpeg", step, cmdline.Windows)
			if len(steps) > 1 {
				line = t.Muted.Render(fmt.Sprintf("Pass %d: ", i+1)) + line
			}
			bottom = append(bottom, line)
		}
	}
	bottom = fit(bottom, width)

	rows := f.ed.lines(t, width)
	// The fields come first, leaving a few lines for the error or the
	// start of the command; the command is cut when it does not fit.
	room := height - len(header) - 2
	want := min(len(rows), max(3, room-3))
	if fits := max(0, room-want); len(bottom) > fits {
		bottom = bottom[:fits]
		if fits > 0 {
			bottom[fits-1] = fit([]string{t.Muted.Render(t.Ellipsis + " (a taller terminal shows the rest)")}, width)[0]
		}
	}
	rows = window(rows, f.ed.focus, want)

	lines := append(header, "")
	lines = append(lines, rows...)
	if len(bottom) > 0 {
		lines = append(lines, "")
		lines = append(lines, bottom...)
	}
	return strings.Join(lines, "\n")
}

// window returns up to n lines of lines, keeping line i in view.
func window(lines []string, i, n int) []string {
	if len(lines) <= n {
		return lines
	}
	start := min(max(0, i-n/2), len(lines)-n)
	return lines[start : start+n]
}

// presetMenu lists the tasks for one or more inputs, then a custom command.
type presetMenu struct {
	inputs   []preset.Context
	presets  []preset.Preset
	problems []error // why each preset cannot work, or nil
	selected int     // index into presets, or len(presets) for the custom command
}

func newPresetMenu(inputs []preset.Context) presetMenu {
	m := presetMenu{inputs: inputs, presets: preset.All()}
	for _, p := range m.presets {
		var problem error
		for _, c := range inputs {
			if err := p.Check(c); err != nil {
				problem = err
				if len(inputs) > 1 {
					problem = fmt.Errorf("%s: %w", filepath.Base(c.Input), err)
				}
				break
			}
		}
		m.problems = append(m.problems, problem)
	}
	var custom error
	if len(inputs) > 1 {
		custom = errors.New("works on one file at a time")
	}
	m.problems = append(m.problems, custom)
	// Start on the first preset that works.
	m.selected = max(0, slices.IndexFunc(m.problems, func(err error) bool { return err == nil }))
	return m
}

// custom reports whether the custom command is selected.
func (m presetMenu) custom() bool { return m.selected == len(m.presets) }

func (m presetMenu) view(t theme.Theme, width, height int) string {
	title := "Choose a task for " + filepath.Base(m.inputs[0].Input)
	if len(m.inputs) > 1 {
		title = fmt.Sprintf("Choose a task for %d files", len(m.inputs))
	}
	lines := fit([]string{t.Heading.Render(title), ""}, width)
	type item struct{ name, desc string }
	var items []item
	for _, p := range m.presets {
		items = append(items, item{p.Name, p.Description})
	}
	items = append(items, item{"Custom command", "Write the FFmpeg command yourself"})
	nameW := 0
	for _, it := range items {
		nameW = max(nameW, ansi.StringWidth(it.name))
	}
	var rows []string
	for i, it := range items {
		marker, name := "  ", fmt.Sprintf("%-*s", nameW, it.name)
		desc := t.Muted.Render(it.desc)
		switch {
		case m.problems[i] != nil:
			name = t.Muted.Render(name)
			desc = t.Muted.Render("can't: " + m.problems[i].Error())
		case i == m.selected:
			name = t.Selected.Render(name)
		}
		if i == m.selected {
			marker = "> "
		}
		line := marker + name + "  " + desc
		if width > 0 {
			line = ansi.Truncate(line, width, t.Ellipsis)
		}
		rows = append(rows, line)
	}
	lines = append(lines, window(rows, m.selected, max(1, height-len(lines)))...)
	return strings.Join(lines, "\n")
}
