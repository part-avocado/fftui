// Package ui is fftui's Bubble Tea interface: an app shell that switches
// between screens and shows a help bar.
package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/part-avocado/fftui/internal/clipboard"
	"github.com/part-avocado/fftui/internal/config"
	"github.com/part-avocado/fftui/internal/ffmpeg"
	"github.com/part-avocado/fftui/internal/job"
	"github.com/part-avocado/fftui/internal/ui/theme"
)

// Screen is one page of the app, such as the file list or the job list.
//
// The active screen receives key presses; every screen receives all other
// messages, so a screen hears about work it started even while another
// screen is showing.
type Screen interface {
	Title() string
	Update(tea.Msg) (Screen, tea.Cmd)
	View(t theme.Theme) string
	// Keys are the screen's own keys, shown in the help bar before the
	// app's keys.
	Keys() []key.Binding
}

// resizeMsg gives every screen the size of the area it draws in.
type resizeMsg struct{ width, height int }

// ffmpegMsg tells every screen what the app knows about FFmpeg: still
// looking (both fields nil), found (tools), or not usable (err).
type ffmpegMsg struct {
	tools *ffmpeg.Tools
	err   error
}

// findFFmpegMsg asks the app to look for FFmpeg again.
type findFFmpegMsg struct{}

// settingsMsg tells every screen the settings changed and were saved.
type settingsMsg struct{ cfg config.Config }

// copiedMsg reports a finished copy to the clipboard.
type copiedMsg struct {
	what string
	err  error
}

// Options configure the app.
type Options struct {
	// Config is the settings from the config file, which the Settings
	// screen edits and saves to ConfigPath.
	Config     config.Config
	ConfigPath string
	// ASCII forces plain-ASCII drawing whatever the config says, as the
	// --ascii flag does.
	ASCII bool
	// FindFFmpeg locates FFmpeg and detects what it supports, using the
	// paths in cfg. It runs at startup, when the user asks to look again,
	// and when the FFmpeg settings change.
	FindFFmpeg func(ctx context.Context, cfg config.Config) (*ffmpeg.Tools, error)
	// Copy puts text on the clipboard with the OS's own tool; nil means
	// clipboard.Copy. The app also sends the text with OSC 52.
	Copy func(string) error
	// Install lists commands that install FFmpeg on this OS, shown when it
	// is missing.
	Install []string
	// Files are the media files given on the command line.
	Files []string
	// Jobs runs the encodes. The caller stops its jobs after the program
	// exits, and sends JobsChanged whenever Jobs.Changed fires.
	Jobs *job.Queue
	// Command is FFmpeg arguments from the command line, run as a job once
	// FFmpeg is found.
	Command []string
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
}

// App is the root Bubble Tea model.
type App struct {
	screens []Screen
	active  int
	keys    keyMap
	help    help.Model
	theme   theme.Theme
	ascii   bool
	width   int
	height  int
	body    resizeMsg // the size last sent to the screens
	cfg     config.Config
	find    func(context.Context, config.Config) (*ffmpeg.Tools, error)
	copy    func(string) error
	jobs    *job.Queue
	// notice is a short message over the help bar, such as "Copied", shown
	// until the next key.
	notice string
	// confirmQuit is set while asking whether to quit with jobs running.
	confirmQuit bool
	dark        bool // the terminal's background is dark
}

// jobsScreenIndex is where New puts the Jobs screen.
const jobsScreenIndex = 1

// capturer is a Screen that can take every key, such as a form while the
// user types in it. The app then keeps only ctrl+c for itself.
type capturer interface{ captures() bool }

func (a App) captures() bool {
	c, ok := a.screens[a.active].(capturer)
	return ok && c.captures()
}

// New returns the app shell.
func New(o Options) App {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	screens := []Screen{
		newFiles(o.Files, o.Install, o.ConfigPath, o.Config.OutputDir),
		newJobs(o.Jobs, o.Command, now),
		newSettings(o.Config, o.ConfigPath),
	}
	if o.Copy == nil {
		o.Copy = clipboard.Copy
	}
	a := App{
		screens: screens,
		keys:    newKeyMap(len(screens)),
		help:    help.New(),
		ascii:   o.ASCII,
		dark:    true, // until the terminal says otherwise
		cfg:     o.Config,
		find:    o.FindFFmpeg,
		copy:    o.Copy,
		jobs:    o.Jobs,
	}
	if o.Command != nil {
		a.active = jobsScreenIndex
	}
	a.setTheme(theme.New(true, a.ascii || a.cfg.ASCII))
	return a
}

func (a *App) setTheme(t theme.Theme) {
	a.theme = t
	a.help.Styles = t.Help
	a.help.ShortSeparator = t.HelpSep
	a.help.Ellipsis = t.Ellipsis
}

// Init asks the terminal for its background color so the theme can match,
// and starts looking for FFmpeg.
func (a App) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, a.findFFmpeg())
}

func (a App) findFFmpeg() tea.Cmd {
	find, cfg := a.find, a.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		tools, err := find(ctx, cfg)
		return ffmpegMsg{tools: tools, err: err}
	}
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		a.dark = msg.IsDark()
		a.setTheme(theme.New(a.dark, a.ascii || a.cfg.ASCII))
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.help.SetWidth(msg.Width)
	case findFFmpegMsg:
		cmds = append(cmds, a.broadcast(ffmpegMsg{}), a.findFFmpeg())
	case queueJobsMsg:
		a.active = jobsScreenIndex
		cmds = append(cmds, a.broadcast(msg))
	case settingsMsg:
		old := a.cfg
		a.cfg = msg.cfg
		a.jobs.SetLimit(a.cfg.Jobs)
		a.setTheme(theme.New(a.dark, a.ascii || a.cfg.ASCII))
		a.notice = "Saved the settings."
		cmds = append(cmds, a.broadcast(msg))
		if old.FFmpegPath != a.cfg.FFmpegPath || old.FFprobePath != a.cfg.FFprobePath {
			cmds = append(cmds, a.broadcast(ffmpegMsg{}), a.findFFmpeg())
		}
	case copyMsg:
		// OSC 52 reaches the terminal even over SSH, but some terminals
		// ignore it, so the OS's own tool copies too where there is one.
		copyText := a.copy
		cmds = append(cmds, tea.SetClipboard(msg.text), func() tea.Msg {
			return copiedMsg{msg.what, copyText(msg.text)}
		})
	case copiedMsg:
		switch {
		case msg.err == nil:
			a.notice = "Copied " + msg.what + "."
		case errors.Is(msg.err, clipboard.ErrNoTool):
			a.notice = "Sent " + msg.what + " to the terminal's clipboard. If pasting does not work, your terminal does not allow it."
		default:
			a.notice = "Sent " + msg.what + " to the terminal's clipboard, but the system clipboard failed: " + msg.err.Error()
		}
	case tea.KeyPressMsg:
		a.notice = ""
		switch {
		case a.confirmQuit:
			switch msg.String() {
			case "y", "q", "ctrl+c":
				return a, tea.Quit
			case "n", "esc":
				a.confirmQuit = false
			}
		case a.captures() && msg.String() != "ctrl+c":
			var cmd tea.Cmd
			a.screens[a.active], cmd = a.screens[a.active].Update(msg)
			cmds = append(cmds, cmd)
		case key.Matches(msg, a.keys.Quit) && a.jobs.Active() > 0:
			a.confirmQuit = true
		case key.Matches(msg, a.keys.Quit):
			return a, tea.Quit
		case key.Matches(msg, a.keys.Next):
			a.active = (a.active + 1) % len(a.screens)
		case key.Matches(msg, a.keys.Prev):
			a.active = (a.active + len(a.screens) - 1) % len(a.screens)
		case key.Matches(msg, a.keys.Jump):
			n, _ := strconv.Atoi(msg.String())
			a.active = n - 1
		case key.Matches(msg, a.keys.Help):
			a.help.ShowAll = !a.help.ShowAll
			if a.help.ShowAll {
				a.keys.Help.SetHelp("?", "close help")
			} else {
				a.keys.Help.SetHelp("?", "more keys")
			}
		default:
			var cmd tea.Cmd
			a.screens[a.active], cmd = a.screens[a.active].Update(msg)
			cmds = append(cmds, cmd)
		}
	default:
		cmds = append(cmds, a.broadcast(msg))
	}
	// The help bar's height depends on the active screen's keys, so the
	// body can change size after any message.
	if w, h := a.bodySize(); (resizeMsg{w, h}) != a.body {
		a.body = resizeMsg{w, h}
		cmds = append(cmds, a.broadcast(a.body))
	}
	return a, tea.Batch(cmds...)
}

func (a *App) broadcast(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, len(a.screens))
	for i, s := range a.screens {
		a.screens[i], cmds[i] = s.Update(msg)
	}
	return tea.Batch(cmds...)
}

func (a App) header() string {
	tabs := []string{a.theme.Title.Render("fftui")}
	for i, s := range a.screens {
		label := fmt.Sprintf("%d %s", i+1, s.Title())
		if i == a.active {
			tabs = append(tabs, a.theme.ActiveTab.Render(label))
		} else {
			tabs = append(tabs, a.theme.Tab.Render(label))
		}
	}
	return strings.Join(tabs, a.theme.TabGap)
}

func (a App) helpBar() string {
	if a.confirmQuit {
		n := a.jobs.Active()
		text := fmt.Sprintf("%d jobs are not finished. Quit and stop them? y quit, n keep working", n)
		if n == 1 {
			text = "1 job is not finished. Quit and stop it? y quit, n keep working"
		}
		return a.theme.Error.Render(strings.Join(fit([]string{text}, a.width), "\n"))
	}
	if a.notice != "" {
		return a.theme.Selected.Render(strings.Join(fit([]string{a.notice}, a.width), "\n"))
	}
	return a.help.View(helpKeys{screen: a.screens[a.active].Keys(), app: a.keys, width: a.width, sep: a.help.ShortSeparator, modal: a.captures()})
}

// bodySize returns the space inside the body's border, or 0, 0 before the
// terminal size is known.
func (a App) bodySize() (width, height int) {
	if a.width <= 0 || a.height <= 0 {
		return 0, 0
	}
	body := a.theme.Body
	height = a.height - lipgloss.Height(a.header()) - lipgloss.Height(a.helpBar()) - body.GetVerticalFrameSize()
	return max(a.width-body.GetHorizontalFrameSize(), 0), max(height, 0)
}

func (a App) View() tea.View {
	header, helpBar := a.header(), a.helpBar()
	body := a.theme.Body
	view := a.screens[a.active].View(a.theme)
	if a.width > 0 && a.height > 0 {
		w, h := a.bodySize()
		// Cut a view that is too tall here, so the border stays whole.
		if lines := fit(strings.Split(view, "\n"), w); len(lines) > h {
			view = strings.Join(lines[:h], "\n")
		}
		h += body.GetVerticalFrameSize()
		body = body.Width(a.width).Height(h).MaxHeight(h)
	}
	content := body.Render(view)

	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, header, content, helpBar))
	v.AltScreen = true
	v.WindowTitle = "fftui"
	return v
}

type keyMap struct {
	Next key.Binding
	Prev key.Binding
	Jump key.Binding
	Help key.Binding
	Quit key.Binding
}

func newKeyMap(screens int) keyMap {
	digits := make([]string, screens)
	for i := range digits {
		digits[i] = strconv.Itoa(i + 1)
	}
	return keyMap{
		Next: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next screen")),
		Prev: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous screen")),
		Jump: key.NewBinding(key.WithKeys(digits...), key.WithHelp(fmt.Sprintf("1-%d", screens), "go to screen")),
		Help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "more keys")),
		Quit: key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// helpKeys puts the active screen's keys before the app's in the help bar.
// The short help always keeps "more keys" and "quit" and leaves out other
// keys that do not fit in width.
type helpKeys struct {
	screen []key.Binding
	app    keyMap
	width  int
	sep    string
	// modal is set while the screen takes every key but ctrl+c.
	modal bool
}

var forceQuit = key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit"))

func (h helpKeys) ShortHelp() []key.Binding {
	itemWidth := func(b key.Binding) int {
		return ansi.StringWidth(b.Help().Key) + 1 + ansi.StringWidth(b.Help().Desc) + ansi.StringWidth(h.sep)
	}
	always := []key.Binding{h.app.Help, h.app.Quit}
	// The jump key is only in the full help, to leave room for screen keys.
	optional := append(slices.Clone(h.screen), h.app.Next)
	if h.modal {
		always, optional = []key.Binding{forceQuit}, h.screen
	}
	used := 0
	for _, b := range always {
		used += itemWidth(b)
	}
	var keys []key.Binding
	for _, b := range optional {
		if w := itemWidth(b); h.width <= 0 || used+w <= h.width {
			keys = append(keys, b)
			used += w
		}
	}
	return append(keys, always...)
}

func (h helpKeys) FullHelp() [][]key.Binding {
	app := []key.Binding{h.app.Next, h.app.Prev, h.app.Jump, h.app.Help, h.app.Quit}
	if h.modal {
		app = []key.Binding{forceQuit}
	}
	if len(h.screen) > 0 {
		return [][]key.Binding{h.screen, app}
	}
	return [][]key.Binding{app}
}
