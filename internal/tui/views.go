package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/pksorensen/vibecast/internal/session"
	"github.com/pksorensen/vibecast/internal/styles"
	"github.com/pksorensen/vibecast/internal/types"
)

// View renders the current state.
func (m Model) View() string {
	switch m.Phase {
	case types.PhaseWaiting:
		return m.viewWaiting()
	case types.PhaseSplash:
		return m.viewSplash()
	case types.PhaseMenu:
		return m.viewMenu()
	case types.PhaseStarting:
		return m.viewStarting()
	case types.PhaseLive:
		if m.PhaseApprovals {
			return m.viewApprovals()
		}
		return m.viewLive()
	case types.PhaseStopping:
		return m.viewStopping()
	case types.PhaseSettings:
		return m.viewSettings()
	case types.PhaseSessions:
		return m.viewSessions()
	}
	return ""
}

func (m Model) viewWaiting() string {
	spin := m.Spinner.View()
	lines := []string{
		"  AGENTICS BROADCAST SYSTEM",
		"  ─────────────────────────────────────────────────────────",
		"",
		fmt.Sprintf("   %s vibecast orchestrator running", spin),
		"",
		"   The menu is in your tmux session.",
		"   Use the tmux window to start streaming.",
		"",
		"  ─────────────────────────────────────────────────────────",
		"  q quit",
	}
	return styles.RenderCRT(lines, false)
}

func (m Model) viewSplash() string {
	lines := styles.CenterBurst(styles.SplashLines, m.SplashFrame, styles.SplashMaxFrames)
	if !m.SplashDone {
		lines[12] = "                                                              "
	}
	return styles.RenderCRT(lines, false)
}

func (m Model) viewMenu() string {
	lines := []string{
		"  AGENTICS BROADCAST SYSTEM          v0.1.0",
		"  ─────────────────────────────────────────────────────────",
		"",
	}

	type menuItem struct {
		label, desc string
	}
	items := []menuItem{
		{"Start Streaming", "Launch tmux + Claude"},
		{"Resume Session", "Pick a previous Claude session"},
		{"Settings", "Configure broadcast options"},
		{"Quit", "Exit vibecast"},
	}

	for i, item := range items {
		if i == m.MenuIndex {
			lines = append(lines, "   ▸ "+item.label)
			lines = append(lines, "     "+item.desc)
		} else {
			lines = append(lines, "     "+item.label)
			lines = append(lines, "     "+item.desc)
		}
		lines = append(lines, "")
	}

	if m.Err != nil {
		lines = append(lines, fmt.Sprintf("  Error: %v", m.Err))
	}

	lines = append(lines,
		"  ─────────────────────────────────────────────────────────",
		"  ↑/↓ navigate  ⏎ select  q quit",
	)

	if !m.TransDone {
		lines = styles.CenterBurst(lines, m.TransFrame, styles.TransMaxFrames)
	}
	return styles.RenderCRT(lines, false)
}

func (m Model) viewSettings() string {
	check := func(b bool) string {
		if b {
			return "x"
		}
		return " "
	}

	lines := []string{
		"  SETTINGS",
		"  ─────────────────────────────────────────────────────────",
		"",
	}

	type settingItem struct {
		label, desc string
		checked     bool
	}
	items := []settingItem{
		{"Share prompts with viewers", "Show Claude prompts in the viewer overlay", m.PromptSharing},
		{"Share project info", "Send workspace/project name to viewers", m.ShareProjectInfo},
	}

	for i, item := range items {
		prefix := "   "
		if i == m.SettingsIndex {
			prefix = "   ▸"
		} else {
			prefix = "    "
		}
		lines = append(lines, fmt.Sprintf("%s [%s] %s", prefix, check(item.checked), item.label))
		lines = append(lines, fmt.Sprintf("         %s", item.desc))
		lines = append(lines, "")
	}

	lines = append(lines,
		"  ─────────────────────────────────────────────────────────",
		"  ↑/↓ navigate  ⏎ toggle  esc back",
	)

	if !m.TransDone {
		lines = styles.CenterBurst(lines, m.TransFrame, styles.TransMaxFrames)
	}
	return styles.RenderCRT(lines, false)
}

func (m Model) viewSessions() string {
	lines := []string{
		"  RESUME SESSION",
		"  ─────────────────────────────────────────────────────────",
		"",
	}

	if len(m.ClaudeSessions) == 0 {
		lines = append(lines, "   No sessions found for this project.")
		lines = append(lines, "")
		lines = append(lines, "   Sessions are stored in ~/.claude/projects/")
	} else {
		maxVisible := 6
		end := m.SessionScroll + maxVisible
		if end > len(m.ClaudeSessions) {
			end = len(m.ClaudeSessions)
		}
		for i := m.SessionScroll; i < end; i++ {
			s := m.ClaudeSessions[i]
			shortID := s.SessionID
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}
			prefix := "    "
			if i == m.SessionIndex {
				prefix = "   ▸"
			}
			timeStr := session.RelativeTime(s.LastUpdated)
			lines = append(lines, fmt.Sprintf("%s %s  %-14s  %d msgs", prefix, shortID, timeStr, s.MessageCount))
			prompt := s.FirstPrompt
			if prompt == "" {
				prompt = "(no prompt)"
			}
			if len(prompt) > 50 {
				prompt = prompt[:50] + "..."
			}
			lines = append(lines, fmt.Sprintf("         %s", prompt))
		}
		if end < len(m.ClaudeSessions) {
			lines = append(lines, fmt.Sprintf("         ... %d more", len(m.ClaudeSessions)-end))
		}
	}

	lines = append(lines,
		"",
		"  ─────────────────────────────────────────────────────────",
		"  ↑/↓ navigate  ⏎ select  esc back",
	)

	if !m.TransDone {
		lines = styles.CenterBurst(lines, m.TransFrame, styles.TransMaxFrames)
	}
	return styles.RenderCRT(lines, m.StreamID != "")
}

func (m Model) viewStarting() string {
	spin := m.Spinner.View()
	lines := []string{
		"  AGENTICS BROADCAST SYSTEM",
		"  ─────────────────────────────────────────────────────────",
		"",
		fmt.Sprintf("   %s Starting broadcast...", spin),
		"",
		"   [✓] Creating tmux session",
		"   [✓] Starting Claude Code",
		fmt.Sprintf("   [%s] Launching ttyd", spin),
		"   [ ] Connecting to server...",
		"   [ ] Establishing broadcast relay",
		"",
		"  ─────────────────────────────────────────────────────────",
		"  > Please wait...",
	}

	if !m.TransDone {
		lines = styles.CenterBurst(lines, m.TransFrame, styles.TransMaxFrames)
	}
	return styles.RenderCRT(lines, false)
}

func (m Model) viewLive() string {
	upStr := m.Uptime.String()
	dotCount := int(m.Uptime.Seconds())%4 + 1
	dots := strings.Repeat("●", dotCount) + strings.Repeat(" ", 4-dotCount)

	var pinLines []string
	if m.PinCode != "" {
		n := len(m.PinCode)
		var top, mid1, mid2, mid3, bot strings.Builder
		top.WriteString("╔")
		mid1.WriteString("║")
		mid2.WriteString("║")
		mid3.WriteString("║")
		bot.WriteString("╚")
		for i := 0; i < n; i++ {
			top.WriteString("═════")
			mid1.WriteString("     ")
			mid2.WriteString(fmt.Sprintf("  %c  ", m.PinCode[i]))
			mid3.WriteString("     ")
			bot.WriteString("═════")
			if i < n-1 {
				top.WriteString("╦")
				mid1.WriteString("║")
				mid2.WriteString("║")
				mid3.WriteString("║")
				bot.WriteString("╩")
			}
		}
		top.WriteString("╗")
		mid1.WriteString("║")
		mid2.WriteString("║")
		mid3.WriteString("║")
		bot.WriteString("╝")

		gridWidth := lipgloss.Width(top.String())
		pad := (styles.CrtInnerWidth - gridWidth) / 2
		if pad < 0 {
			pad = 0
		}
		prefix := strings.Repeat(" ", pad)
		pinLines = []string{
			prefix + top.String(),
			prefix + mid1.String(),
			prefix + mid2.String(),
			prefix + mid3.String(),
			prefix + bot.String(),
		}
	}

	lines := []string{
		"  LIVE            AGENTICS BROADCAST SYSTEM",
		"  ─────────────────────────────────────────────────────────",
		"",
		fmt.Sprintf("   Stream:  %-8s    Viewers: %d", m.StreamID, m.ViewerCount),
		fmt.Sprintf("   Uptime:  %s", upStr),
		"",
	}
	if len(pinLines) > 0 {
		lines = append(lines, pinLines...)
		lines = append(lines, "")
		urlLine := fmt.Sprintf("   LINK:  %s", m.StreamURL)
		lines = append(lines, urlLine)
		lines = append(lines, "")
	}

	if m.ShowChat && len(m.ChatMessages) > 0 {
		lines = append(lines, "  -- Chat -------------------------------------------------")
		start := len(m.ChatMessages) - 3
		if start < 0 {
			start = 0
		}
		for _, msg := range m.ChatMessages[start:] {
			var chatLine string
			if msg.Type == "system" {
				chatLine = fmt.Sprintf("  * %s", msg.Text)
			} else if msg.Type == "message" {
				chatLine = fmt.Sprintf("  %s: %s", msg.Username, msg.Text)
			}
			if len(chatLine) > 60 {
				chatLine = chatLine[:60]
			}
			lines = append(lines, chatLine)
		}
		lines = append(lines, "  ---------------------------------------------------------")
	} else {
		lines = append(lines, fmt.Sprintf("   Broadcasting %s", dots))
	}

	if m.ImageApprovals > 0 {
		lines = append(lines, fmt.Sprintf("   +%d pending image approval(s)", m.ImageApprovals))
	}

	if len(m.Panes) > 1 {
		lines = append(lines, fmt.Sprintf("   Panes: %d  Active: %s", len(m.Panes), m.Panes[m.ActivePaneIdx].Name))
	}

	lines = append(lines,
		"",
		"  ─────────────────────────────────────────────────────────",
		"  w attach  s stop  i images  q quit",
	)

	if !m.TransDone {
		lines = styles.CenterBurst(lines, m.TransFrame, styles.TransMaxFrames)
	}

	return styles.RenderCRT(lines, true)
}

func (m Model) viewApprovals() string {
	lines := []string{
		"  IMAGE APPROVALS",
		"  ─────────────────────────────────────────────────────────",
		"",
	}

	for i, img := range m.PendingImages {
		cursor := "   "
		if i == m.ApprovalIndex {
			cursor = "  >"
		}
		caption := img.Caption
		if len(caption) > 35 {
			caption = caption[:35] + "..."
		}
		if caption == "" {
			caption = "(no caption)"
		}
		ago := time.Since(time.Unix(img.Timestamp, 0)).Round(time.Second)
		line := fmt.Sprintf("%s [%d] %s  \"%s\"  %s ago", cursor, i+1, img.ImageID, caption, ago)
		if len(line) > styles.CrtInnerWidth {
			line = line[:styles.CrtInnerWidth]
		}
		lines = append(lines, line)
	}

	lines = append(lines,
		"",
		"  ─────────────────────────────────────────────────────────",
		"  up/dn navigate  enter approve  x reject  esc back",
	)

	return styles.RenderCRT(lines, true)
}

func (m Model) viewStopping() string {
	spin := m.Spinner.View()
	upStr := m.Uptime.String()
	lines := []string{
		"  AGENTICS BROADCAST SYSTEM",
		"  ─────────────────────────────────────────────────────────",
		"",
		fmt.Sprintf("   %s Stopping broadcast...", spin),
		"",
		"   [✓] Disconnecting relay",
		"   [✓] Stopping ttyd",
		fmt.Sprintf("   [%s] Closing tmux session...", spin),
		"",
		"   Session summary:",
		fmt.Sprintf("   Duration: %-8s  Viewers: %d", upStr, m.ViewerCount),
		"",
		"  ─────────────────────────────────────────────────────────",
		"  > Goodbye!",
	}

	if !m.TransDone {
		lines = styles.CenterBurst(lines, m.TransFrame, styles.TransMaxFrames)
	}
	return styles.RenderCRT(lines, false)
}
