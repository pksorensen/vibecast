package fkeybar

import (
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/pksorensen/vibecast/internal/styles"
)

// LobbyTickMsg drives the splash → transition → lobby animation and the
// continuous radio-wave pulse in the steady lobby state.
type LobbyTickMsg struct{}

const (
	lobbySplashFrames = 40 // ~2 s at 50 ms/tick
	lobbyTransFrames  = 25 // ~1.25 s
)

// ScheduleLobbyTick returns a tea.Cmd that fires LobbyTickMsg after 50ms.
func ScheduleLobbyTick() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return LobbyTickMsg{} })
}

// AGENTIC LIVE block-letter splash logo (only shown during the splash phase).
var splashLogo = []string{
	" █████╗  ██████╗ ███████╗███╗   ██╗████████╗██╗ ██████╗",
	"██╔══██╗██╔════╝ ██╔════╝████╗  ██║╚══██╔══╝██║██╔════╝",
	"███████║██║  ███╗█████╗  ██╔██╗ ██║   ██║   ██║██║     ",
	"██╔══██║██║   ██║██╔══╝  ██║╚██╗██║   ██║   ██║██║     ",
	"██║  ██║╚██████╔╝███████╗██║ ╚████║   ██║   ██║╚██████╗",
	"╚═╝  ╚═╝ ╚═════╝ ╚══════╝╚═╝  ╚═══╝   ╚═╝   ╚═╝ ╚═════╝",
	"",
	"               ██╗     ██╗██╗   ██╗███████╗            ",
	"               ██║     ██║██║   ██║██╔════╝            ",
	"               ██║     ██║██║   ██║█████╗              ",
	"               ██║     ██║╚██╗ ██╔╝██╔══╝              ",
	"               ███████╗██║ ╚████╔╝ ███████╗            ",
	"               ╚══════╝╚═╝  ╚═══╝  ╚══════╝            ",
}

// ASCII broadcast tower (the anchor of the lobby layout).
var towerArt = []string{
	"      ▲      ",
	"     /│\\     ",
	"    / │ \\    ",
	"   /  │  \\   ",
	"  /   │   \\  ",
	"  \\   │   /  ",
	"   \\  │  /   ",
	"    \\ │ /    ",
	"     \\│/     ",
	"      │      ",
	"     ─┴─     ",
}

func easeOutCubic(t float64) float64 {
	t -= 1
	return t*t*t + 1
}

// viewLobby renders the splash → transition → menu layout. Phase advancement
// and frame counter live on InfoModel (lobbyFrame, lobbyPhase, lobbyTransFrame)
// and are updated on LobbyTickMsg in Update().
func (m InfoModel) viewLobby(menuTitles, menuDescs []string) string {
	if m.Width == 0 || m.Height == 0 {
		return ""
	}

	w, h := m.Width, m.Height
	rows := make([][]string, h)
	for r := range rows {
		rows[r] = make([]string, w)
		for c := range rows[r] {
			rows[r][c] = " "
		}
	}

	logoH := len(splashLogo)
	logoW := lipgloss.Width(splashLogo[0])
	towerW := lipgloss.Width(towerArt[0])
	towerH := len(towerArt)

	// Splash positions (centered)
	splashLogoR := (h-logoH-towerH)/2 - 3
	if splashLogoR < 1 {
		splashLogoR = 1
	}
	splashLogoC := (w - logoW) / 2
	splashTowerR := splashLogoR + logoH + 1
	splashTowerC := (w - towerW) / 2

	// Lobby header (single-line, replaces the big logo once we're in the menu)
	headerText := "AGENTICS BROADCAST SYSTEM"
	headerSub := "─────────────────────────"
	headerR := 1
	headerC := (w - len(headerText)) / 2
	contentTopR := headerR + 3

	// Lobby tower position (left side, vertically centered in content area)
	lobbyTowerC := w / 10
	lobbyTowerR := contentTopR + 2

	var towerR, towerC int
	var menuSlide float64
	switch m.lobbyPhase {
	case 0:
		towerR, towerC = splashTowerR, splashTowerC
		menuSlide = 0
	case 1:
		t := float64(m.lobbyTransFrame) / float64(lobbyTransFrames)
		t = easeOutCubic(t)
		towerR = splashTowerR + int(float64(lobbyTowerR-splashTowerR)*t)
		towerC = splashTowerC + int(float64(lobbyTowerC-splashTowerC)*t)
		menuSlide = t
	default:
		towerR, towerC = lobbyTowerR, lobbyTowerC
		menuSlide = 1
	}

	towerTipX := towerC + towerW/2
	towerTipY := towerR

	// Wave-clipping: in the lobby phase, never let waves cross into the menu lane.
	menuFinalC := w/2 - 2
	if menuFinalC < lobbyTowerC+towerW+8 {
		menuFinalC = lobbyTowerC + towerW + 8
	}
	menuLeftEdge := w
	if m.lobbyPhase == 1 {
		menuLeftEdge = w - int(float64(w-menuFinalC)*menuSlide)
	} else if m.lobbyPhase == 2 {
		menuLeftEdge = menuFinalC
	}

	// 1. Radio waves
	maxRadius := math.Min(float64(w)/2, float64(h)) * 0.85
	for waveOffset := 0; waveOffset < 3; waveOffset++ {
		waveFrame := (m.lobbyFrame + waveOffset*15) % 45
		radius := float64(waveFrame) / 45.0 * maxRadius
		if radius < 4 {
			continue
		}
		alpha := 1.0 - float64(waveFrame)/45.0
		var color string
		switch {
		case alpha > 0.7:
			color = "#FF6B00"
		case alpha > 0.4:
			color = "#AA4400"
		default:
			color = "#552200"
		}
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
		drawArcClipped(rows, towerTipX, towerTipY, radius, st, w, h, menuLeftEdge-2)
	}

	// 2. Tower
	towerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	for i, line := range towerArt {
		for j, r := range []rune(line) {
			rr, cc := towerR+i, towerC+j
			if rr >= 0 && rr < h && cc >= 0 && cc < w && r != ' ' {
				rows[rr][cc] = towerStyle.Render(string(r))
			}
		}
	}

	// 3. Splash phase: big logo. Lobby/transition: compact text header.
	if m.lobbyPhase == 0 {
		logoStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
		for i, line := range splashLogo {
			for j, r := range []rune(line) {
				rr, cc := splashLogoR+i, splashLogoC+j
				if rr >= 0 && rr < h && cc >= 0 && cc < w {
					rows[rr][cc] = logoStyle.Render(string(r))
				}
			}
		}
	} else {
		hStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
		if m.lobbyPhase == 1 && menuSlide < 0.4 {
			hStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#552200")).Bold(true)
		}
		subStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
		for i, r := range []rune(headerText) {
			cc := headerC + i
			if cc >= 0 && cc < w && headerR < h {
				rows[headerR][cc] = hStyle.Render(string(r))
			}
		}
		for i, r := range []rune(headerSub) {
			cc := headerC + i
			if cc >= 0 && cc < w && headerR+1 < h {
				rows[headerR+1][cc] = subStyle.Render(string(r))
			}
		}
	}

	// 4. BROADCASTING tag below tower (lobby phases only)
	if m.lobbyPhase >= 1 {
		tag := "▲  BROADCASTING  ▲"
		tagR := towerR + towerH + 1
		tagC := towerC + (towerW-len(tag))/2
		var color string
		if menuSlide > 0.5 {
			color = "#FF6B00"
		} else {
			color = "#552200"
		}
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
		for i, r := range []rune(tag) {
			cc := tagC + i
			if cc >= 0 && cc < w && tagR >= 0 && tagR < h {
				rows[tagR][cc] = st.Render(string(r))
			}
		}
	}

	// 5. Menu items on the right (slides in from offscreen during transition)
	if m.lobbyPhase >= 1 && len(menuTitles) > 0 {
		menuStartC := w
		menuC := menuStartC + int(float64(menuFinalC-menuStartC)*menuSlide)
		menuR := contentTopR + 2

		titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
		dimTitle := lipgloss.NewStyle().Foreground(lipgloss.Color("#CCCCCC"))
		descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
		hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))

		for i, title := range menuTitles {
			marker := "  "
			st := dimTitle
			if i == m.MenuIndex && m.lobbyPhase == 2 {
				marker = "▸ "
				st = titleStyle
			}
			rendered := st.Render(marker + title)
			placeStyledRunes(rows, menuR+i*3, menuC, rendered, w, h)
			if i < len(menuDescs) {
				descRendered := descStyle.Render("    " + menuDescs[i])
				placeStyledRunes(rows, menuR+i*3+1, menuC, descRendered, w, h)
			}
		}

		hintR := menuR + len(menuTitles)*3
		hint := hintStyle.Render("↑↓ Navigate   ⏎ Select   q Quit")
		placeStyledRunes(rows, hintR, menuC, hint, w, h)
	}

	var b strings.Builder
	for r := 0; r < h-1; r++ {
		b.WriteString(strings.Join(rows[r], ""))
		b.WriteString("\n")
	}
	return b.String()
}

// drawArcClipped paints an upper-half arc of dots centered at (cx, cy) at the
// given radius, but only into cells where x ≤ maxX (so radio waves don't cross
// into the menu lane).
func drawArcClipped(rows [][]string, cx, cy int, radius float64, st lipgloss.Style, w, h, maxX int) {
	steps := int(radius * 8)
	if steps < 12 {
		steps = 12
	}
	for i := 0; i <= steps; i++ {
		t := math.Pi + float64(i)/float64(steps)*math.Pi
		x := cx + int(math.Round(math.Cos(t)*radius*2))
		y := cy + int(math.Round(math.Sin(t)*radius))
		if x >= 0 && x < w && x <= maxX && y >= 0 && y < h && rows[y][x] == " " {
			rows[y][x] = st.Render(")")
		}
	}
}

// placeStyledRunes writes a lipgloss-rendered string starting at (row, col)
// rune by rune, preserving the single SGR open/close pair around each rune.
// Assumes the input was produced by a single Style.Render() call.
func placeStyledRunes(rows [][]string, row, col int, styled string, w, h int) {
	if row < 0 || row >= h {
		return
	}
	open, mid, closeSGR := splitStyledSGR(styled)
	for i, r := range []rune(mid) {
		c := col + i
		if c < 0 || c >= w {
			continue
		}
		rows[row][c] = open + string(r) + closeSGR
	}
}

func splitStyledSGR(s string) (string, string, string) {
	startEsc := strings.Index(s, "\x1b[")
	if startEsc < 0 {
		return "", s, ""
	}
	endOpen := strings.IndexByte(s[startEsc:], 'm')
	if endOpen < 0 {
		return "", s, ""
	}
	openEnd := startEsc + endOpen + 1
	lastEsc := strings.LastIndex(s, "\x1b[")
	if lastEsc <= startEsc {
		return s[:openEnd], s[openEnd:], ""
	}
	return s[:openEnd], s[openEnd:lastEsc], s[lastEsc:]
}
