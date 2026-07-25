package tui

import "charm.land/lipgloss/v2"

// theme centralizes the restrained, evidence-first visual language. Color is
// always paired with text and shape so the interface remains legible when a
// terminal down-samples or removes styling.
type theme struct {
	accent    lipgloss.Style
	title     lipgloss.Style
	muted     lipgloss.Style
	focused   lipgloss.Style
	success   lipgloss.Style
	warning   lipgloss.Style
	danger    lipgloss.Style
	info      lipgloss.Style
	separator lipgloss.Style
	footer    lipgloss.Style
}

// newTheme selects contrast-appropriate colors for the terminal background
// while keeping the same semantic roles in both variants.
func newTheme(dark bool) theme {
	foreground := lipgloss.Color("#D7DEE9")
	muted := lipgloss.Color("#8290A6")
	accent := lipgloss.Color("#7DD3C7")
	focusBackground := lipgloss.Color("#183B3A")
	success := lipgloss.Color("#8BD49C")
	warning := lipgloss.Color("#E6C177")
	danger := lipgloss.Color("#E78284")
	info := lipgloss.Color("#8CAAEE")
	if !dark {
		foreground = lipgloss.Color("#25324A")
		muted = lipgloss.Color("#667085")
		accent = lipgloss.Color("#087F8C")
		focusBackground = lipgloss.Color("#DDF3F1")
		success = lipgloss.Color("#257A3E")
		warning = lipgloss.Color("#8A5D00")
		danger = lipgloss.Color("#B42318")
		info = lipgloss.Color("#2457A6")
	}
	return theme{
		accent:    lipgloss.NewStyle().Foreground(accent).Bold(true),
		title:     lipgloss.NewStyle().Foreground(foreground).Bold(true),
		muted:     lipgloss.NewStyle().Foreground(muted),
		focused:   lipgloss.NewStyle().Foreground(foreground).Background(focusBackground).Bold(true),
		success:   lipgloss.NewStyle().Foreground(success).Bold(true),
		warning:   lipgloss.NewStyle().Foreground(warning).Bold(true),
		danger:    lipgloss.NewStyle().Foreground(danger).Bold(true),
		info:      lipgloss.NewStyle().Foreground(info).Bold(true),
		separator: lipgloss.NewStyle().Foreground(muted),
		footer:    lipgloss.NewStyle().Foreground(muted),
	}
}
