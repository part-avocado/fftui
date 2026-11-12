package ui

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/part-avocado/fftui/internal/cmdline"
	"github.com/part-avocado/fftui/internal/job"
	"github.com/part-avocado/fftui/internal/preset"
	"github.com/part-avocado/fftui/internal/ui/theme"
)

// rawKey is the raw command's field.
const rawKey = "command"

// rawForm edits an FFmpeg command as text, for anything the presets do not
// cover, and runs it as it is written.
type rawForm struct {
	c       preset.Context // the input the command was written for
	program string
	ed      editor
	args    []string // the command without "ffmpeg", once it parses
	err     error
	keys    formKeys
}

func newRawForm(c preset.Context, program string, args []string) *rawForm {
	r := &rawForm{c: c, program: program, keys: newFormKeys()}
	r.keys.Start.SetHelp("enter", "run")
	r.ed.rows = []preset.Field{{Key: rawKey, Label: "Command", Kind: preset.Text}}
	r.ed.values = preset.Values{rawKey: cmdline.Join("ffmpeg", args, cmdline.Windows)}
	r.ed.cursor = len([]rune(r.ed.value()))
	r.refresh()
	return r
}

// customArgs is the command the custom task starts from.
func customArgs(c preset.Context, dir string) []string {
	base := filepath.Base(c.Input)
	ext := filepath.Ext(base)
	if dir == "" {
		dir = filepath.Dir(c.Input)
	}
	return []string{"-i", c.Input, filepath.Join(dir, strings.TrimSuffix(base, ext)+"-custom"+ext)}
}

func (r *rawForm) refresh() {
	args, err := cmdline.Split(r.ed.value())
	// The command can start with ffmpeg, as copied commands do.
	if err == nil && len(args) > 0 {
		name := strings.ToLower(filepath.Base(args[0]))
		if strings.TrimSuffix(name, ".exe") == "ffmpeg" {
			args = args[1:]
		}
	}
	if err == nil && len(args) == 0 {
		err = errors.New("write the FFmpeg arguments, such as -i input.mp4 output.mkv")
	}
	r.args, r.err = args, err
}

func (r *rawForm) keyList() []key.Binding {
	keys := []key.Binding{r.keys.Start}
	if r.err == nil {
		keys = append(keys, r.keys.Copy)
	}
	return append(keys, r.keys.Back)
}

// update handles a key or paste. It returns done when the raw form should
// close, and a command when it started the job or copied the command.
func (r *rawForm) update(msg tea.Msg) (done bool, cmd tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		r.ed.insert(msg.Content)
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, r.keys.Back):
			return true, nil
		case key.Matches(msg, r.keys.Start):
			if r.err != nil {
				return false, nil
			}
			return true, r.start()
		case key.Matches(msg, r.keys.Copy):
			if r.err == nil {
				return false, copyCmd("the command", cmdline.Join("ffmpeg", r.args, cmdline.Windows))
			}
			return false, nil
		case msg.String() == "up" || msg.String() == "down" || msg.String() == "tab" || msg.String() == "shift+tab":
			return false, nil
		}
		r.ed.key(msg)
	}
	r.refresh()
	return false, nil
}

func (r *rawForm) start() tea.Cmd {
	args := r.args
	spec := job.Spec{Name: filepath.Base(args[len(args)-1]), Program: r.program, Steps: [][]string{args}}
	// The percentage needs the output's length: the input's, unless the
	// command cuts or repeats it.
	if in, ok := durationInput(args); ok && filepath.Clean(in) == filepath.Clean(r.c.Input) {
		spec.Duration = r.c.Media.Duration
	}
	return func() tea.Msg { return queueJobsMsg{[]job.Spec{spec}} }
}

func (r *rawForm) view(t theme.Theme, width, height int) string {
	lines := fit([]string{
		t.Heading.Render("Custom command") + t.Muted.Render(" "+t.HelpSep+" "+filepath.Base(r.c.Input)),
		t.Muted.Render("Runs exactly as written. Quote arguments with spaces with \" or '."),
		"",
		withCursor(t, r.ed.value(), r.ed.cursor),
		"",
	}, width)
	if r.err != nil {
		lines = append(lines, fit([]string{t.Error.Render("Can't run: ") + r.err.Error()}, width)...)
	} else {
		note := "FFmpeg will not overwrite a file unless the command has -y."
		if slices.Contains(r.args, "-y") {
			note = "-y is set, so FFmpeg overwrites the output if it exists."
		}
		lines = append(lines, fit([]string{t.Muted.Render(note)}, width)...)
	}
	return strings.Join(lines, "\n")
}
