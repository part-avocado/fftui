package ui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/part-avocado/fftui/internal/preset"
	"github.com/part-avocado/fftui/internal/ui/theme"
)

// editor edits a column of fields: up and down move between them, left and
// right cycle a choice, and typing edits text. The preset forms, the raw
// command, and the settings all use one.
type editor struct {
	rows   []preset.Field
	values preset.Values
	focus  int
	cursor int // position in the focused text field, in runes
}

func (e *editor) focused() preset.Field { return e.rows[e.focus] }

func (e *editor) value() string { return e.values[e.focused().Key] }

// setRows replaces the fields, staying on the field with the same key.
func (e *editor) setRows(rows []preset.Field) {
	key := ""
	if e.focus < len(e.rows) {
		key = e.focused().Key
	}
	e.rows = rows
	if i := slices.IndexFunc(rows, func(r preset.Field) bool { return r.Key == key }); i >= 0 {
		e.focus = i
	}
	e.focus = min(e.focus, len(rows)-1)
	e.cursor = min(e.cursor, len([]rune(e.value())))
}

func (e *editor) move(by int) {
	e.focus = (e.focus + by + len(e.rows)) % len(e.rows)
	e.cursor = len([]rune(e.value()))
}

// key applies a key press, and returns the key of the field whose value it
// changed, or "".
func (e *editor) key(k tea.KeyPressMsg) string {
	field := e.focused()
	switch k.String() {
	case "up", "shift+tab":
		e.move(-1)
		return ""
	case "down", "tab":
		e.move(1)
		return ""
	}
	if field.Kind == preset.Choice {
		step := 0
		switch k.String() {
		case "left":
			step = -1
		case "right", "space":
			step = 1
		}
		if step == 0 || len(field.Options) == 0 {
			return ""
		}
		i := slices.IndexFunc(field.Options, func(o preset.Option) bool { return o.Value == e.value() })
		i = (i + step + len(field.Options)) % len(field.Options)
		return e.set(field.Options[i].Value)
	}

	r := []rune(e.value())
	e.cursor = min(e.cursor, len(r))
	switch k.String() {
	case "left", "ctrl+b":
		e.cursor = max(0, e.cursor-1)
	case "right", "ctrl+f":
		e.cursor = min(len(r), e.cursor+1)
	case "ctrl+left", "alt+left", "alt+b":
		e.cursor = wordStart(r, e.cursor)
	case "ctrl+right", "alt+right", "alt+f":
		e.cursor = wordEnd(r, e.cursor)
	case "home", "ctrl+a":
		e.cursor = 0
	case "end", "ctrl+e":
		e.cursor = len(r)
	case "backspace":
		if e.cursor > 0 {
			e.cursor--
			return e.set(string(slices.Delete(r, e.cursor, e.cursor+1)))
		}
	case "delete", "ctrl+d":
		if e.cursor < len(r) {
			return e.set(string(slices.Delete(r, e.cursor, e.cursor+1)))
		}
	case "ctrl+w", "alt+backspace":
		start := wordStart(r, e.cursor)
		r = slices.Delete(r, start, e.cursor)
		e.cursor = start
		return e.set(string(r))
	case "ctrl+u":
		e.cursor = 0
		return e.set("")
	default:
		if k.Text != "" {
			return e.insert(k.Text)
		}
	}
	return ""
}

// insert types s at the cursor of a text field. Pasted text loses its final
// line break, and its other line breaks become spaces, joining a command
// split over lines with \ back into one.
func (e *editor) insert(s string) string {
	if e.focused().Kind != preset.Text {
		return ""
	}
	s = strings.TrimRight(s, "\r\n")
	s = strings.NewReplacer("\\\r\n", " ", "\\\n", " ", "\r\n", " ", "\r", " ", "\n", " ").Replace(s)
	r := []rune(e.value())
	e.cursor = min(e.cursor, len(r))
	in := []rune(s)
	r = slices.Insert(r, e.cursor, in...)
	e.cursor += len(in)
	return e.set(string(r))
}

func (e *editor) set(v string) string {
	key := e.focused().Key
	if e.values[key] == v {
		return ""
	}
	e.values[key] = v
	return key
}

func wordStart(r []rune, i int) int {
	for i > 0 && unicode.IsSpace(r[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(r[i-1]) {
		i--
	}
	return i
}

func wordEnd(r []rune, i int) int {
	for i < len(r) && unicode.IsSpace(r[i]) {
		i++
	}
	for i < len(r) && !unicode.IsSpace(r[i]) {
		i++
	}
	return i
}

// withCursor draws s with the cursor at rune index c.
func withCursor(t theme.Theme, s string, c int) string {
	rs := []rune(s)
	c = min(c, len(rs))
	under, rest := " ", ""
	if c < len(rs) {
		under, rest = string(rs[c]), string(rs[c+1:])
	}
	return string(rs[:c]) + t.Cursor.Render(under) + rest
}

// lines draws one line per field, each cut to width.
func (e *editor) lines(t theme.Theme, width int) []string {
	labelW := 0
	for _, r := range e.rows {
		labelW = max(labelW, ansi.StringWidth(r.Label))
	}
	left, right := "‹ ", " ›"
	if t.ASCII {
		left, right = "< ", " >"
	}
	var lines []string
	for i, r := range e.rows {
		focused := i == e.focus
		marker, label := "  ", fmt.Sprintf("%-*s", labelW, r.Label)
		if focused {
			marker, label = "> ", t.Selected.Render(label)
		}
		value := e.values[r.Key]
		switch {
		case r.Kind == preset.Choice:
			for _, o := range r.Options {
				if o.Value == value {
					value = o.Label
				}
			}
			if focused && len(r.Options) > 1 {
				value = left + value + right
			}
		case focused:
			// A long value scrolls to keep the cursor in view.
			rs, c := []rune(value), min(e.cursor, len([]rune(value)))
			room := width - 2 - labelW - 2 - 1
			if width > 0 && len(rs) >= room && room > 10 {
				start := max(0, c-room+ansi.StringWidth(t.Ellipsis)+1)
				if start > 0 {
					value, c = t.Ellipsis+string(rs[start:]), c-start+len([]rune(t.Ellipsis))
				}
			}
			value = withCursor(t, value, c)
		}
		line := marker + label + "  " + value
		if r.Hint != "" {
			line += t.Muted.Render("  " + r.Hint)
		}
		if width > 0 {
			line = ansi.Truncate(line, width, t.Ellipsis)
		}
		lines = append(lines, line)
	}
	return lines
}
