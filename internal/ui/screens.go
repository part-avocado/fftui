package ui

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/part-avocado/fftui/internal/config"
	"github.com/part-avocado/fftui/internal/preset"
	"github.com/part-avocado/fftui/internal/ui/theme"
)

// settings shows the settings and the FFmpeg that was found, and edits the
// settings, saving them to the config file.
type settings struct {
	cfg   config.Config
	path  string
	ff    ffmpegMsg
	width int

	// ed is set while editing; err says why the edit cannot be saved.
	ed   *editor
	err  error
	keys settingsKeys
}

type settingsKeys struct {
	Edit, Up, Change, Save, Cancel key.Binding
}

func newSettings(cfg config.Config, path string) settings {
	return settings{cfg: cfg, path: path, keys: settingsKeys{
		Edit:   key.NewBinding(key.WithKeys("e", "enter"), key.WithHelp("e", "edit settings")),
		Up:     key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("up/down", "field")),
		Change: key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("left/right", "change")),
		Save:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save")),
		Cancel: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}}
}

func (s settings) Title() string { return "Settings" }

func (s settings) captures() bool { return s.ed != nil }

func (s settings) Keys() []key.Binding {
	if s.ed == nil {
		return []key.Binding{s.keys.Edit}
	}
	keys := []key.Binding{s.keys.Up}
	if s.ed.focused().Kind == preset.Choice {
		keys = append(keys, s.keys.Change)
	}
	return append(keys, s.keys.Save, s.keys.Cancel)
}

// settingRows are the fields of the settings, keyed by their names in the
// config file.
var settingRows = []preset.Field{
	{Key: "jobs", Label: "jobs", Kind: preset.Text, Hint: "FFmpeg jobs that run at once"},
	{Key: "output_dir", Label: "output_dir", Kind: preset.Text, Hint: "empty: next to the input"},
	{Key: "ffmpeg_path", Label: "ffmpeg_path", Kind: preset.Text, Hint: "empty: find it"},
	{Key: "ffprobe_path", Label: "ffprobe_path", Kind: preset.Text, Hint: "empty: next to ffmpeg"},
	{Key: "ascii", Label: "ascii", Kind: preset.Choice, Options: []preset.Option{
		{Value: "false", Label: "false"}, {Value: "true", Label: "true (plain ASCII drawing)"}}},
}

func (s settings) values() preset.Values {
	return preset.Values{
		"jobs":         strconv.Itoa(s.cfg.Jobs),
		"output_dir":   s.cfg.OutputDir,
		"ffmpeg_path":  s.cfg.FFmpegPath,
		"ffprobe_path": s.cfg.FFprobePath,
		"ascii":        strconv.FormatBool(s.cfg.ASCII),
	}
}

// parse reads the edited values, or says what is wrong with them.
func parseSettings(v preset.Values) (config.Config, error) {
	jobs, err := strconv.Atoi(strings.TrimSpace(v["jobs"]))
	if err != nil || jobs < 1 {
		return config.Config{}, errors.New("jobs must be a whole number, 1 or more")
	}
	cfg := config.Config{
		Jobs:        jobs,
		OutputDir:   strings.TrimSpace(v["output_dir"]),
		FFmpegPath:  strings.TrimSpace(v["ffmpeg_path"]),
		FFprobePath: strings.TrimSpace(v["ffprobe_path"]),
		ASCII:       v["ascii"] == "true",
	}
	if cfg.OutputDir != "" {
		if info, err := os.Stat(expandHome(cfg.OutputDir)); err != nil || !info.IsDir() {
			return cfg, fmt.Errorf("output_dir: the folder %s does not exist", cfg.OutputDir)
		}
	}
	return cfg, cfg.Validate()
}

func (s settings) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case resizeMsg:
		s.width = msg.width
	case ffmpegMsg:
		s.ff = msg
	case settingsMsg:
		s.cfg = msg.cfg
	case tea.PasteMsg:
		if s.ed != nil {
			s.ed.insert(msg.Content)
			_, s.err = parseSettings(s.ed.values)
		}
	case tea.KeyPressMsg:
		if s.ed == nil {
			if key.Matches(msg, s.keys.Edit) {
				s.ed = &editor{rows: settingRows, values: s.values()}
				s.ed.cursor = len([]rune(s.ed.value()))
				s.err = nil
			}
			return s, nil
		}
		switch {
		case key.Matches(msg, s.keys.Cancel):
			s.ed, s.err = nil, nil
		case key.Matches(msg, s.keys.Save):
			cfg, err := parseSettings(s.ed.values)
			if err == nil {
				err = config.Save(s.path, cfg)
			}
			if s.err = err; err != nil {
				return s, nil
			}
			s.ed = nil
			return s, func() tea.Msg { return settingsMsg{cfg} }
		default:
			s.ed.key(msg)
			_, s.err = parseSettings(s.ed.values)
		}
	}
	return s, nil
}

func (s settings) View(t theme.Theme) string {
	lines := []string{t.Heading.Render("Settings"), "", "Config file: " + s.path, ""}
	if s.ed != nil {
		lines = append(lines, s.ed.lines(t, s.width)...)
		lines = append(lines, "")
		if s.err != nil {
			lines = append(lines, t.Error.Render("Can't save: ")+s.err.Error())
		} else {
			lines = append(lines, t.Muted.Render("enter saves to the config file; esc keeps the old settings."))
		}
	} else {
		orDefault := func(v, def string) string {
			if v == "" {
				return def
			}
			return v
		}
		lines = append(lines,
			fmt.Sprintf("jobs          %d", s.cfg.Jobs),
			"output_dir    "+orDefault(s.cfg.OutputDir, "(next to the input)"),
			"ffmpeg_path   "+orDefault(s.cfg.FFmpegPath, "(find automatically)"),
			"ffprobe_path  "+orDefault(s.cfg.FFprobePath, "(find automatically)"),
			fmt.Sprintf("ascii         %t", s.cfg.ASCII),
			"",
			t.Muted.Render("Press e to change these. Changes apply at once, including to queued jobs."),
		)
	}
	lines = append(lines, "", t.Heading.Render("FFmpeg"))
	switch tools := s.ff.tools; {
	case s.ff.err != nil:
		lines = append(lines, "Not usable: "+s.ff.err.Error(), t.Muted.Render("The Files screen explains how to fix this."))
	case tools == nil:
		lines = append(lines, "Looking for FFmpeg"+t.Ellipsis)
	default:
		var apis []string
		hw := tools.Caps.HardwareEncoders()
		for _, e := range hw {
			if api := e.HardwareAPI(); !slices.Contains(apis, api) {
				apis = append(apis, api)
			}
		}
		hwText := "none"
		if len(hw) > 0 {
			hwText = fmt.Sprintf("%d (%s)", len(hw), strings.Join(apis, ", "))
		}
		lines = append(lines,
			"ffmpeg        "+tools.FFmpeg+" (from "+tools.Source+")",
			"ffprobe       "+tools.FFprobe,
			"version       "+tools.Caps.Version.String,
			"hw encoders   "+hwText,
		)
		if !tools.Caps.Version.AtLeast(4, 4) {
			lines = append(lines, t.Error.Render("This FFmpeg is older than 4.4, the oldest version fftui supports."))
		}
		if s.cfg.FFmpegPath != "" && tools.Source != "config ffmpeg_path" {
			lines = append(lines, t.Muted.Render("ffmpeg_path is not used: "+tools.Source+" comes first."))
		}
	}
	return strings.Join(lines, "\n")
}
