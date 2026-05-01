package fkeybar

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/pksorensen/vibecast/internal/styles"
	"github.com/pksorensen/vibecast/internal/types"
)

// ── Messages ─────────────────────────────────────────────────────────────────

type statusTickMsg struct{}
type statusUpdatedMsg struct {
	Status *StatusResponse
	Panes  []types.PaneStatus
}
type statusErrorMsg struct{}

type sessionEntry struct {
	ID      string `json:"sessionId"`
	Prompt  string `json:"firstPrompt"`
	Updated string `json:"lastUpdated"`
	Count   int    `json:"messageCount"`
}

// ── Info Model (full-screen info window) ─────────────────────────────────────

// InfoModel runs as the sole process in the "info" tmux window.
// Pre-stream: shows menu (Start Streaming, Resume, Settings, Quit).
// Post-stream: shows live stats (PIN, URL, viewers, uptime).
type InfoModel struct {
	Width    int
	Height   int

	// State from control socket polling
	Phase     string
	SessionID string
	Viewers   int
	Uptime    string
	URL       string
	PinCode   string
	Panes     []types.PaneStatus

	// Menu state (pre-stream)
	MenuIndex int
	menuItems []string
	menuDescs []string
	SubView   string // "", "settings", "sessions"

	// Lobby splash/transition animation state
	lobbyFrame      int
	lobbyPhase      int // 0 splash, 1 transition, 2 steady
	lobbyTransFrame int

	// Settings
	PromptSharing    bool
	ShareProjectInfo bool
	SettingsIndex    int

	// Sessions
	Sessions      []sessionEntry
	SessionIndex  int
	SessionScroll int

	client *Client
	VSCode bool
}

// NewInfoModel creates the full-screen info model.
func NewInfoModel(sessionID string) InfoModel {
	return InfoModel{
		SessionID: sessionID,
		Phase:     "menu",
		menuItems: []string{"Start Streaming", "Resume Session", "Settings", "Quit"},
		menuDescs: []string{
			"Launch tmux + Claude",
			"Pick a previous Claude session",
			"Configure broadcast options",
			"Exit vibecast",
		},
		PromptSharing:    true,
		ShareProjectInfo: true,
		client:           NewClient(),
		VSCode:           os.Getenv("TERM_PROGRAM") == "vscode",
	}
}

func (m InfoModel) Init() tea.Cmd {
	return tea.Batch(m.pollStatus(), scheduleStatusTick(), ScheduleLobbyTick())
}

func (m InfoModel) pollStatus() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		status, err := client.GetStatus()
		if err != nil {
			return statusErrorMsg{}
		}
		panes, _ := client.GetPanes()
		return statusUpdatedMsg{Status: status, Panes: panes}
	}
}

func (m InfoModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg.String())

	case statusTickMsg:
		return m, tea.Batch(m.pollStatus(), scheduleStatusTick())

	case LobbyTickMsg:
		m.lobbyFrame++
		switch m.lobbyPhase {
		case 0:
			if m.lobbyFrame >= lobbySplashFrames {
				m.lobbyPhase = 1
				m.lobbyTransFrame = 0
			}
		case 1:
			m.lobbyTransFrame++
			if m.lobbyTransFrame >= lobbyTransFrames {
				m.lobbyPhase = 2
			}
		}
		return m, ScheduleLobbyTick()

	case statusUpdatedMsg:
		if msg.Status != nil {
			m.Phase = msg.Status.Phase
			m.Viewers = msg.Status.Viewers
			m.Uptime = msg.Status.Uptime
			if msg.Status.SessionID != "" {
				m.SessionID = msg.Status.SessionID
			}
			if msg.Status.URL != "" {
				m.URL = msg.Status.URL
			}
			if msg.Status.PinCode != "" {
				m.PinCode = msg.Status.PinCode
			}
		}
		if msg.Panes != nil {
			m.Panes = msg.Panes
		}
		return m, nil

	case statusErrorMsg:
		return m, nil
	}
	return m, nil
}

func (m InfoModel) handleKey(key string) (tea.Model, tea.Cmd) {
	// When live, the info screen is read-only (F-keys handled by tmux bindings)
	if m.Phase == "live" || m.Phase == "starting" {
		if key == "q" || key == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}

	// Pre-stream: menu navigation
	switch m.SubView {
	case "settings":
		return m.handleSettingsKey(key)
	case "sessions":
		return m.handleSessionsKey(key)
	default:
		return m.handleMenuKey(key)
	}
}

func (m InfoModel) handleMenuKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.MenuIndex > 0 {
			m.MenuIndex--
		}
	case "down", "j":
		if m.MenuIndex < len(m.menuItems)-1 {
			m.MenuIndex++
		}
	case "enter", " ":
		switch m.MenuIndex {
		case 0: // Start Streaming
			go m.client.PostStartStream(m.PromptSharing, m.ShareProjectInfo)
			m.Phase = "starting"
		case 1: // Resume Session
			m.SubView = "sessions"
		case 2: // Settings
			m.SubView = "settings"
		case 3: // Quit
			return m, tea.Quit
		}
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m InfoModel) handleSettingsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.SettingsIndex > 0 {
			m.SettingsIndex--
		}
	case "down", "j":
		if m.SettingsIndex < 1 {
			m.SettingsIndex++
		}
	case "enter", " ":
		if m.SettingsIndex == 0 {
			m.PromptSharing = !m.PromptSharing
		} else {
			m.ShareProjectInfo = !m.ShareProjectInfo
		}
	case "esc":
		m.SubView = ""
	}
	return m, nil
}

func (m InfoModel) handleSessionsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.SessionIndex > 0 {
			m.SessionIndex--
			if m.SessionIndex < m.SessionScroll {
				m.SessionScroll = m.SessionIndex
			}
		}
	case "down", "j":
		if m.SessionIndex < len(m.Sessions)-1 {
			m.SessionIndex++
			if m.SessionIndex >= m.SessionScroll+6 {
				m.SessionScroll = m.SessionIndex - 5
			}
		}
	case "enter":
		// TODO: resume selected session
		m.SubView = ""
	case "esc":
		m.SubView = ""
	}
	return m, nil
}

// ── Info Views ───────────────────────────────────────────────────────────────

func (m InfoModel) View() string {
	if m.Width == 0 || m.Height == 0 {
		return ""
	}

	var content string
	showFKeyRow := false
	switch {
	case m.Phase == "live":
		content = m.viewLive()
		showFKeyRow = true
	case m.Phase == "starting":
		content = m.viewStarting()
	case m.SubView == "settings":
		content = m.viewSettings()
	case m.SubView == "sessions":
		content = m.viewSessions()
	default:
		// New tower-anchored lobby: includes its own ↑↓/⏎/q hint, fills the screen,
		// and animates radio waves continuously. No bottom F-key bar needed here.
		return m.viewLobby(m.menuItems, m.menuDescs)
	}

	if !showFKeyRow {
		return content
	}

	fkeyRow := m.renderInfoFKeyRow()
	contentLines := strings.Count(content, "\n") + 1
	fkeyLines := strings.Count(fkeyRow, "\n") + 1
	padding := m.Height - contentLines - fkeyLines
	if padding < 0 {
		padding = 0
	}

	return content + strings.Repeat("\n", padding) + fkeyRow
}

func (m InfoModel) renderInfoFKeyRow() string {
	r := styles.RenderFKeyEntry
	var entries []string
	if m.VSCode {
		entries = []string{
			r("^b1", " Coding Agent"),
			r("^b2", " New"),
			r("^b3", " ◀"),
			r("^b4", " ▶"),
			r("^b5", " Close"),
			r("^b6", " Restart"),
			r("^b9", " Help"),
			r("^b0", " Stop"),
		}
	} else {
		entries = []string{
			r("F1", " Coding Agent"),
			r("F2", " New"),
			r("F3", " ◀ Agent"),
			r("F4", " Agent ▶"),
			r("F5", " Close"),
			r("F6", " Restart"),
			r("F9", " Help"),
			r("F10", " Stop"),
		}
	}
	return justifyAcrossWidth(entries, m.Width)
}

// justifyAcrossWidth distributes entries across the full terminal width by
// padding the gaps between them. Falls back to single-space joins if the
// width is unknown or too narrow.
func justifyAcrossWidth(entries []string, width int) string {
	if len(entries) == 0 {
		return ""
	}
	totalW := 0
	for _, e := range entries {
		totalW += lipgloss.Width(e)
	}
	gaps := len(entries) - 1
	// Reserve a single-space margin on each side.
	avail := width - 2 - totalW
	if width <= 0 || gaps == 0 || avail <= gaps {
		return " " + strings.Join(entries, " ")
	}
	gapW := avail / gaps
	extra := avail - gapW*gaps
	var b strings.Builder
	b.WriteString(" ")
	for i, e := range entries {
		b.WriteString(e)
		if i < gaps {
			pad := gapW
			if i < extra {
				pad++
			}
			b.WriteString(strings.Repeat(" ", pad))
		}
	}
	return b.String()
}

func (m InfoModel) viewMenu() string {
	titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dimStyle := lipgloss.NewStyle().Faint(true)

	lines := []string{
		"",
		" " + titleStyle.Render("AGENTICS BROADCAST SYSTEM") + dimStyle.Render("  v0.3.2-local"),
		" " + dimStyle.Render("─────────────────────────────────────────"),
		"",
	}

	descs := []string{
		"Launch tmux + Claude",
		"Pick a previous Claude session",
		"Configure broadcast options",
		"Exit vibecast",
	}

	for i, item := range m.menuItems {
		if i == m.MenuIndex {
			lines = append(lines, "  "+styles.FKeyAction.Render("▸ "+item))
			lines = append(lines, "    "+dimStyle.Render(descs[i]))
		} else {
			lines = append(lines, "    "+item)
			lines = append(lines, "    "+dimStyle.Render(descs[i]))
		}
		lines = append(lines, "")
	}

	r := styles.RenderFKeyEntry
	lines = append(lines, " "+strings.Join([]string{r("↑↓", " Navigate"), r("⏎", " Select"), r("q", " Quit")}, "  "))

	return strings.Join(lines, "\n")
}

func (m InfoModel) viewStarting() string {
	titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dimStyle := lipgloss.NewStyle().Faint(true)
	lines := []string{
		"",
		" " + titleStyle.Render("AGENTICS BROADCAST SYSTEM"),
		" " + dimStyle.Render("─────────────────────────────────────────"),
		"",
		"  Starting broadcast...",
		"",
		"  [✓] Creating tmux session",
		"  [✓] Starting Claude Code",
		"  [ ] Connecting to server...",
		"",
		"  > Please wait...",
	}
	return strings.Join(lines, "\n")
}

func (m InfoModel) viewLive() string {
	titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dimStyle := lipgloss.NewStyle().Faint(true)
	liveStyle := lipgloss.NewStyle().Foreground(styles.SuccessColor).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#666666")).Bold(true)

	width := m.Width
	if width <= 0 {
		width = 80
	}

	// Full-width header: ● LIVE on the left, AGENTICS BROADCAST SYSTEM on the right.
	leftLabel := liveStyle.Render("● LIVE")
	rightLabel := titleStyle.Render("AGENTICS BROADCAST SYSTEM")
	header := joinLeftRight(leftLabel, rightLabel, width)
	divider := " " + dimStyle.Render(strings.Repeat("─", maxInt(1, width-2)))

	// Build the left column (SESSION / JOIN / AGENTS).
	left := []string{
		" " + labelStyle.Render("SESSION"),
		fmt.Sprintf("  ID:       %s", m.SessionID),
		fmt.Sprintf("  Uptime:   %s", m.Uptime),
		fmt.Sprintf("  Viewers:  %s", liveStyle.Render(fmt.Sprintf("%d", m.Viewers))),
		"",
	}

	if m.PinCode != "" || m.URL != "" {
		left = append(left, " "+labelStyle.Render("JOIN"))
		if m.URL != "" {
			left = append(left, "  "+m.URL)
		}
		if m.PinCode != "" {
			left = append(left, "")
			bigLines := renderBigPIN(m.PinCode)
			for _, l := range bigLines {
				left = append(left, "  "+l)
			}
			spaced := ""
			for i, c := range m.PinCode {
				if i > 0 {
					spaced += "       "
				}
				spaced += fmt.Sprintf("  %c   ", c)
			}
			left = append(left, "  "+dimStyle.Render(spaced))
		}
		left = append(left, "")
	}

	if len(m.Panes) > 0 {
		left = append(left, " "+labelStyle.Render(fmt.Sprintf("AGENTS  (%d)", len(m.Panes))))
		for _, p := range m.Panes {
			marker := "  "
			label := fmt.Sprintf("Claude — %s", p.Name)
			if p.Active {
				marker = " ▸"
				label = titleStyle.Render(label)
			} else {
				label = lipgloss.NewStyle().Foreground(lipgloss.Color("#CCCCCC")).Render(label)
			}
			left = append(left, marker+" "+label)
		}
		left = append(left, "")
	}

	// QR code for the join URL, if we have one.
	var qr []string
	qrW := 0
	if m.URL != "" {
		qr = renderQRCode(m.URL)
		qrW = qrModuleWidth(m.URL)
	}

	// Decide layout: side-by-side if there is enough room next to the left column.
	// Left column needs ~50 cols; QR is qrW cols wide. Leave 4 cols of gutter.
	const leftColW = 50
	const gutter = 4
	canSideBySide := qrW > 0 && width >= leftColW+gutter+qrW

	var body []string
	if canSideBySide {
		// Place QR starting two rows below the header so it lines up with SESSION.
		const qrTopOffset = 1
		n := len(left)
		if len(qr)+qrTopOffset > n {
			n = len(qr) + qrTopOffset
		}
		body = make([]string, n)
		for i := 0; i < n; i++ {
			var l string
			if i < len(left) {
				l = left[i]
			}
			line := padToWidth(l, leftColW+gutter)
			qi := i - qrTopOffset
			if qi >= 0 && qi < len(qr) {
				line += qr[qi]
			}
			body[i] = line
		}
	} else {
		body = left
		if len(qr) > 0 {
			// Place QR centered below the JOIN URL/PIN block.
			body = append(body, "")
			pad := (width - qrW) / 2
			if pad < 2 {
				pad = 2
			}
			leftPad := strings.Repeat(" ", pad)
			for _, l := range qr {
				body = append(body, leftPad+l)
			}
			body = append(body, "")
		}
	}

	out := []string{"", header, divider, ""}
	out = append(out, body...)
	return strings.Join(out, "\n")
}

// joinLeftRight returns a single line of the given width with `left` flush-left
// and `right` flush-right (with one-space margins on each side).
func joinLeftRight(left, right string, width int) string {
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	gap := width - leftW - rightW - 2
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right + " "
}

// padToWidth right-pads s with spaces so that its visible width equals w.
// Returns s unchanged if it is already wider than w.
func padToWidth(s string, w int) string {
	cur := lipgloss.Width(s)
	if cur >= w {
		return s
	}
	return s + strings.Repeat(" ", w-cur)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// bigDigits maps characters to 5-line tall block representations.
// Each character is rendered using █ and spaces, 6 chars wide.
var bigLetters = map[byte][5]string{
	'0': {"█████ ", "█   █ ", "█   █ ", "█   █ ", "█████ "},
	'1': {"  █   ", "  █   ", "  █   ", "  █   ", "  █   "},
	'2': {"█████ ", "    █ ", "█████ ", "█     ", "█████ "},
	'3': {"█████ ", "    █ ", "█████ ", "    █ ", "█████ "},
	'4': {"█   █ ", "█   █ ", "█████ ", "    █ ", "    █ "},
	'5': {"█████ ", "█     ", "█████ ", "    █ ", "█████ "},
	'6': {"█████ ", "█     ", "█████ ", "█   █ ", "█████ "},
	'7': {"█████ ", "    █ ", "    █ ", "    █ ", "    █ "},
	'8': {"█████ ", "█   █ ", "█████ ", "█   █ ", "█████ "},
	'9': {"█████ ", "█   █ ", "█████ ", "    █ ", "█████ "},
	'A': {"█████ ", "█   █ ", "█████ ", "█   █ ", "█   █ "},
	'B': {"████  ", "█   █ ", "████  ", "█   █ ", "████  "},
	'C': {"█████ ", "█     ", "█     ", "█     ", "█████ "},
	'D': {"████  ", "█   █ ", "█   █ ", "█   █ ", "████  "},
	'E': {"█████ ", "█     ", "████  ", "█     ", "█████ "},
	'F': {"█████ ", "█     ", "████  ", "█     ", "█     "},
	'G': {"█████ ", "█     ", "█ ███ ", "█   █ ", "█████ "},
	'H': {"█   █ ", "█   █ ", "█████ ", "█   █ ", "█   █ "},
	'I': {"█████ ", "  █   ", "  █   ", "  █   ", "█████ "},
	'J': {"█████ ", "    █ ", "    █ ", "█   █ ", "█████ "},
	'K': {"█   █ ", "█  █  ", "███   ", "█  █  ", "█   █ "},
	'L': {"█     ", "█     ", "█     ", "█     ", "█████ "},
	'M': {"█   █ ", "██ ██ ", "█ █ █ ", "█   █ ", "█   █ "},
	'N': {"█   █ ", "██  █ ", "█ █ █ ", "█  ██ ", "█   █ "},
	'O': {"█████ ", "█   █ ", "█   █ ", "█   █ ", "█████ "},
	'P': {"█████ ", "█   █ ", "█████ ", "█     ", "█     "},
	'Q': {"█████ ", "█   █ ", "█   █ ", "█  █  ", "████  "},
	'R': {"█████ ", "█   █ ", "████  ", "█  █  ", "█   █ "},
	'S': {"█████ ", "█     ", "█████ ", "    █ ", "█████ "},
	'T': {"█████ ", "  █   ", "  █   ", "  █   ", "  █   "},
	'U': {"█   █ ", "█   █ ", "█   █ ", "█   █ ", "█████ "},
	'V': {"█   █ ", "█   █ ", "█   █ ", " █ █  ", "  █   "},
	'W': {"█   █ ", "█   █ ", "█ █ █ ", "██ ██ ", "█   █ "},
	'X': {"█   █ ", " █ █  ", "  █   ", " █ █  ", "█   █ "},
	'Y': {"█   █ ", " █ █  ", "  █   ", "  █   ", "  █   "},
	'Z': {"█████ ", "   █  ", "  █   ", " █    ", "█████ "},
}

// renderBigPIN renders a PIN code as large block letters (5 lines tall).
func renderBigPIN(pin string) []string {
	rows := [5]strings.Builder{}
	for i := 0; i < len(pin); i++ {
		ch := pin[i]
		if ch >= 'a' && ch <= 'z' {
			ch -= 32 // uppercase
		}
		glyph, ok := bigLetters[ch]
		if !ok {
			glyph = [5]string{"      ", "      ", "  ?   ", "      ", "      "}
		}
		if i > 0 {
			for r := 0; r < 5; r++ {
				rows[r].WriteString("  ") // gap between letters
			}
		}
		for r := 0; r < 5; r++ {
			rows[r].WriteString(glyph[r])
		}
	}
	result := make([]string, 5)
	for r := 0; r < 5; r++ {
		result[r] = rows[r].String()
	}
	return result
}

func (m InfoModel) viewSettings() string {
	titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dimStyle := lipgloss.NewStyle().Faint(true)

	check := func(b bool) string {
		if b {
			return "x"
		}
		return " "
	}

	lines := []string{
		"",
		" " + titleStyle.Render("SETTINGS"),
		" " + dimStyle.Render("─────────────────────────────────────────"),
		"",
	}

	items := []struct{ label, desc string; checked bool }{
		{"Share prompts with viewers", "Show Claude prompts in the viewer overlay", m.PromptSharing},
		{"Share project info", "Send workspace/project name to viewers", m.ShareProjectInfo},
	}

	for i, it := range items {
		prefix := "   "
		if i == m.SettingsIndex {
			prefix = "  ▸"
		}
		lines = append(lines, prefix+" ["+check(it.checked)+"] "+it.label)
		lines = append(lines, "      "+dimStyle.Render(it.desc))
		lines = append(lines, "")
	}

	r := styles.RenderFKeyEntry
	lines = append(lines, " "+strings.Join([]string{r("↑↓", " Navigate"), r("⏎", " Toggle"), r("Esc", " Back")}, "  "))

	return strings.Join(lines, "\n")
}

func (m InfoModel) viewSessions() string {
	titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dimStyle := lipgloss.NewStyle().Faint(true)

	lines := []string{
		"",
		" " + titleStyle.Render("RESUME SESSION"),
		" " + dimStyle.Render("─────────────────────────────────────────"),
		"",
	}

	if len(m.Sessions) == 0 {
		lines = append(lines, "  No sessions found for this project.")
		lines = append(lines, "")
		lines = append(lines, "  "+dimStyle.Render("Sessions are stored in ~/.claude/projects/"))
	} else {
		end := m.SessionScroll + 6
		if end > len(m.Sessions) {
			end = len(m.Sessions)
		}
		for i := m.SessionScroll; i < end; i++ {
			s := m.Sessions[i]
			shortID := s.ID
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}
			prefix := "   "
			if i == m.SessionIndex {
				prefix = "  ▸"
			}
			lines = append(lines, prefix+" "+shortID+"  "+s.Updated)
			prompt := s.Prompt
			if prompt == "" {
				prompt = "(no prompt)"
			}
			if len(prompt) > 50 {
				prompt = prompt[:50] + "..."
			}
			lines = append(lines, "      "+dimStyle.Render(prompt))
		}
	}

	lines = append(lines, "")
	r := styles.RenderFKeyEntry
	lines = append(lines, " "+strings.Join([]string{r("↑↓", " Navigate"), r("⏎", " Select"), r("Esc", " Back")}, "  "))

	return strings.Join(lines, "\n")
}

// ── Compact Bar Model (2-line bottom pane in workspace windows) ──────────────

// BarModel is the 2-line persistent F-key bar at the bottom of workspace windows.
type BarModel struct {
	Width  int
	Height int
	Panes  []types.PaneStatus

	client *Client
	VSCode bool
}

// NewBarModel creates a 2-line workspace fkeybar.
func NewBarModel(sessionID string) BarModel {
	return BarModel{
		client: NewClient(),
		VSCode: os.Getenv("TERM_PROGRAM") == "vscode",
	}
}

func (m BarModel) Init() tea.Cmd {
	return tea.Batch(m.pollPanes(), scheduleStatusTick())
}

func (m BarModel) pollPanes() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		panes, _ := client.GetPanes()
		return statusUpdatedMsg{Panes: panes}
	}
}

func (m BarModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		return m, nil

	case statusTickMsg:
		return m, tea.Batch(m.pollPanes(), scheduleStatusTick())

	case statusUpdatedMsg:
		if msg.Panes != nil {
			m.Panes = msg.Panes
		}
		return m, nil

	case tea.KeyMsg:
		// Compact bar doesn't handle keys — F-keys go via tmux bindings
		return m, nil
	}
	return m, nil
}

func (m BarModel) View() string {
	if m.Width == 0 {
		return ""
	}
	var lines []string
	if len(m.Panes) > 1 {
		lines = append(lines, m.renderPaneTabs())
	}
	lines = append(lines, m.renderFKeyRow())
	return strings.Join(lines, "\n")
}

func (m BarModel) renderPaneTabs() string {
	var parts []string
	for _, pane := range m.Panes {
		name := pane.Name
		if len(name) > 12 {
			name = name[:12]
		}
		if pane.Active {
			parts = append(parts, styles.FKeyAction.Render("▸ "+name))
		} else {
			parts = append(parts, lipgloss.NewStyle().Faint(true).Render(name))
		}
	}
	return " " + strings.Join(parts, " │ ")
}

func (m BarModel) renderFKeyRow() string {
	r := styles.RenderFKeyEntry
	if m.VSCode {
		return " " + strings.Join([]string{
			r("^b1", " Info"),
			r("^b2", " New"),
			r("^b3", " ◀ Agent"),
			r("^b4", " Agent ▶"),
			r("^b5", " Close"),
			r("^b6", " Restart"),
			r("^b9", " Help"),
			r("^b0", " Stop"),
		}, " ")
	}
	return " " + strings.Join([]string{
		r("F1", " Info"),
		r("F2", " New"),
		r("F3", " ◀ Agent"),
		r("F4", " Agent ▶"),
		r("F5", " Close"),
		r("F6", " Restart"),
		r("F9", " Help"),
		r("F10", " Stop"),
	}, " ")
}

// ── Help Model (full-screen help window) ─────────────────────────────────────

// HelpModel shows all keybindings and tips as a static help screen.
type HelpModel struct {
	Width  int
	Height int
	VSCode bool
}

// NewHelpModel creates the help screen model.
func NewHelpModel() HelpModel {
	return HelpModel{
		VSCode: os.Getenv("TERM_PROGRAM") == "vscode",
	}
}

func (m HelpModel) Init() tea.Cmd {
	// Keepalive tick so the program doesn't exit
	return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return statusTickMsg{} })
}

func (m HelpModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
	case statusTickMsg:
		return m, tea.Tick(30*time.Second, func(time.Time) tea.Msg { return statusTickMsg{} })
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m HelpModel) View() string {
	if m.Width == 0 {
		return ""
	}

	title := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	heading := lipgloss.NewStyle().Bold(true).Underline(true)
	key := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dim := lipgloss.NewStyle().Faint(true)

	prefix := "F"
	if m.VSCode {
		prefix = "^b"
	}
	fk := func(num string) string {
		return key.Render(prefix + num)
	}

	lines := []string{
		"",
		" " + title.Render("AGENTIC LIVE") + "  " + dim.Render("Keyboard Reference"),
		" " + strings.Repeat("─", 50),
		"",
		" " + heading.Render("Broadcast Controls"),
		"",
		"   " + fk("1") + "  Toggle between Info and most recent Coding Agent",
		"   " + fk("2") + "  New Coding Agent (spawn additional Claude)",
		"   " + fk("3") + "  Previous Coding Agent (skips Info / Help)",
		"   " + fk("4") + "  Next Coding Agent (skips Info / Help)",
		"   " + fk("5") + "  Close current Coding Agent",
		"   " + fk("6") + "  Restart Claude in current Coding Agent",
	}
	if m.VSCode {
		lines = append(lines, "   "+fk("9")+"  This help screen")
		lines = append(lines, "   "+fk("0")+"  Stop broadcasting")
	} else {
		lines = append(lines, "   "+fk("9")+"   This help screen")
		lines = append(lines, "   "+fk("10")+"  Stop broadcasting")
	}
	lines = append(lines,
		"",
		" "+heading.Render("tmux Essentials"),
		"",
		"   "+key.Render("^b d")+"      Detach from session (vibecast keeps running)",
		"   "+key.Render("^b z")+"      Zoom current pane (toggle fullscreen)",
		"   "+key.Render("^b [")+"      Scroll mode (use arrows/PgUp/PgDn, "+key.Render("q")+" to exit)",
		"   "+key.Render("^b c")+"      New tmux window",
		"   "+key.Render("^b ,")+"      Rename current window",
		"   "+key.Render("^b w")+"      Window picker",
		"   "+key.Render("^b t")+"      Show clock",
		"",
		" "+heading.Render("Claude Code"),
		"",
		"   "+key.Render("Enter")+"     Send message to Claude",
		"   "+key.Render("Esc")+"       Cancel current generation",
		"   "+key.Render("/help")+"     Show Claude Code help",
		"   "+key.Render("/clear")+"    Clear conversation history",
		"   "+key.Render("/compact")+"  Compact conversation (reduce context)",
		"   "+key.Render("/cost")+"     Show token usage and cost",
		"   "+key.Render("/doctor")+"   Check environment health",
		"   "+key.Render("/model")+"    Switch Claude model",
		"   "+key.Render("Shift+Tab")+" Cycle permission mode",
		"",
		" "+heading.Render("Tips"),
		"",
		"   "+dim.Render("•")+" Viewers see your terminal live — they see what you see",
		"   "+dim.Render("•")+" The Info screen shows your join code and stream link",
		"   "+dim.Render("•")+" Multiple agents: use "+fk("2")+" to spawn, "+fk("3")+"/"+fk("4")+" to switch",
		"   "+dim.Render("•")+" Set "+key.Render("VIBECAST_DEBUG=1")+" for verbose logging",
		"",
	)

	// F-key row at bottom
	r := styles.RenderFKeyEntry
	var fkeyRow string
	if m.VSCode {
		fkeyRow = " " + strings.Join([]string{
			r("^b1", " Info"),
			r("^b9", " Help"),
			r("^b0", " Stop"),
		}, "  ")
	} else {
		fkeyRow = " " + strings.Join([]string{
			r("F1", " Info"),
			r("F9", " Help"),
			r("F10", " Stop"),
		}, "  ")
	}

	content := strings.Join(lines, "\n")
	contentLines := strings.Count(content, "\n") + 1
	fkeyLines := strings.Count(fkeyRow, "\n") + 1
	padding := m.Height - contentLines - fkeyLines
	if padding < 0 {
		padding = 0
	}

	return content + strings.Repeat("\n", padding) + fkeyRow
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func scheduleStatusTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return statusTickMsg{}
	})
}
