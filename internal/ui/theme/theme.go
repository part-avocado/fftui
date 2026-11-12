// Package theme holds fftui's styles and the plain-ASCII fallback for
// terminals or fonts without box-drawing characters.
package theme

import (
	"charm.land/bubbles/v2/help"
	"charm.land/lipgloss/v2"
)

// Theme is the set of styles every screen draws with. Colors are written in
// true color; Bubble Tea downsamples them to what the terminal supports and
// drops them when NO_COLOR is set.
type Theme struct {
	ASCII bool

	Title     lipgloss.Style
	Tab       lipgloss.Style
	ActiveTab lipgloss.Style
	TabGap    string
	Body      lipgloss.Style
	Heading   lipgloss.Style
	Muted     lipgloss.Style
	Selected  lipgloss.Style
	Error     lipgloss.Style
	Cursor    lipgloss.Style // the text cursor in a field
	Help      help.Styles
	HelpSep   string
	Ellipsis  string
	// Check marks a file chosen for a batch.
	Check string
	// BarFull and BarEmpty draw progress bars.
	BarFull, BarEmpty string
}

// New returns the theme for a dark or light terminal background, using only
// ASCII characters when ascii is true.
func New(isDark, ascii bool) Theme {
	c := lipgloss.LightDark(isDark)
	accent := c(lipgloss.Color("#5A3FC0"), lipgloss.Color("#B4A1FF"))
	muted := c(lipgloss.Color("#6B6B6B"), lipgloss.Color("#8A8A8A"))
	red := c(lipgloss.Color("#B3261E"), lipgloss.Color("#FF6B6B"))
	border := lipgloss.RoundedBorder()
	t := Theme{
		ASCII:    ascii,
		TabGap:   " ",
		HelpSep:  " • ",
		Ellipsis: "…",
		Check:    "✓",
		BarFull:  "█",
		BarEmpty: "░",
	}
	if ascii {
		border = lipgloss.ASCIIBorder()
		t.HelpSep = " | "
		t.Ellipsis = "..."
		t.Check = "*"
		t.BarFull, t.BarEmpty = "#", "-"
	}

	t.Title = lipgloss.NewStyle().Bold(true).Foreground(accent).Padding(0, 1)
	t.Tab = lipgloss.NewStyle().Foreground(muted).Padding(0, 1)
	t.ActiveTab = lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	t.Body = lipgloss.NewStyle().Border(border).BorderForeground(accent).Padding(0, 1)
	t.Heading = lipgloss.NewStyle().Bold(true)
	t.Muted = lipgloss.NewStyle().Foreground(muted)
	t.Selected = lipgloss.NewStyle().Bold(true).Foreground(accent)
	t.Error = lipgloss.NewStyle().Bold(true).Foreground(red)
	t.Cursor = lipgloss.NewStyle().Reverse(true)
	t.Help = help.DefaultStyles(isDark)
	return t
}
