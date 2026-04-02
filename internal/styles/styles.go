package styles

import (
	mathrand "math/rand"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Brand colors
var (
	AccentColor  = lipgloss.Color("#FF6B00")
	SuccessColor = lipgloss.Color("#00FF88")

	CrtStyle = lipgloss.NewStyle().Foreground(AccentColor)
	LiveGlow = lipgloss.NewStyle().Foreground(SuccessColor).Bold(true)

	FKeyLabel  = lipgloss.NewStyle().Reverse(true)
	FKeyAction = lipgloss.NewStyle().Foreground(AccentColor)
)

// RenderFKeyEntry renders an htop-style F-key entry: inverted key number + accent action label.
func RenderFKeyEntry(key, action string) string {
	return FKeyLabel.Render(key) + FKeyAction.Render(action)
}

const CrtInnerWidth = 62

// CrtPad pads a string to exactly CrtInnerWidth visible characters.
func CrtPad(s string) string {
	w := lipgloss.Width(s)
	if w >= CrtInnerWidth {
		return s
	}
	return s + strings.Repeat(" ", CrtInnerWidth-w)
}

// RenderCRT wraps content lines in the retro CRT monitor frame.
func RenderCRT(lines []string, onAir bool) string {
	fc := CrtStyle.Render
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(fc("  ╔════════════════════════════════════════════════════════════════════╗") + "\n")
	b.WriteString(fc("  ║  ┌──────────────────────────────────────────────────────────────┐  ║") + "\n")
	for _, line := range lines {
		b.WriteString(fc("  ║  │") + CrtPad(line) + fc("│  ║") + "\n")
	}
	b.WriteString(fc("  ║  └──────────────────────────────────────────────────────────────┘  ║") + "\n")
	if onAir {
		b.WriteString(fc("  ╚════════════════════════════════╗") + " " + LiveGlow.Render("ON AIR") + " " + fc("╔══════════════════════════╝") + "\n")
		b.WriteString(fc("                                  ╚══════════╝") + "\n")
	} else {
		b.WriteString(fc("  ╚════════════════════════════════════════════════════════════════════╝") + "\n")
	}
	return b.String()
}

// CenterBurst applies a center-outward reveal animation.
func CenterBurst(lines []string, frame, maxFrames int) []string {
	if frame >= maxFrames {
		return lines
	}
	center := CrtInnerWidth / 2
	radius := (frame * (CrtInnerWidth / 2)) / maxFrames

	noiseChars := []rune("░▒▓╔╗╚╝═║┌┐└┘─│")
	result := make([]string, len(lines))
	for i, line := range lines {
		runes := []rune(line)
		for len(runes) < CrtInnerWidth {
			runes = append(runes, ' ')
		}
		if len(runes) > CrtInnerWidth {
			runes = runes[:CrtInnerWidth]
		}
		out := make([]rune, CrtInnerWidth)
		for j := 0; j < CrtInnerWidth; j++ {
			dist := j - center
			if dist < 0 {
				dist = -dist
			}
			if dist <= radius {
				out[j] = runes[j]
			} else if dist <= radius+2 {
				out[j] = noiseChars[mathrand.Intn(len(noiseChars))]
			} else {
				out[j] = ' '
			}
		}
		result[i] = string(out)
	}
	return result
}

// SplashLines is the splash screen content.
var SplashLines = []string{
	"",
	"",
	"       A G E N T I C S",
	"          L I V E",
	"",
	"     ─ BROADCAST SYSTEM ─",
	"",
	"",
	"",
	"  ─────────────────────────────────────────────────────────",
	"  Terms of use: https://agentics.dk/tos",
	"",
	"        Press ENTER to accept",
	"",
}

const (
	SplashMaxFrames = 20
	TransMaxFrames  = 20
)
