package fkeybar

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/skip2/go-qrcode"
)

// renderQRCode renders text as a terminal QR code using upper-half-block
// characters (▀) so each character cell encodes two vertical modules. Dark
// modules are forced to a black bg/fg and light modules to white, so the QR
// scans cleanly regardless of the terminal theme.
//
// Returns nil on error or empty input.
func renderQRCode(text string) []string {
	if text == "" {
		return nil
	}
	qr, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return nil
	}
	matrix := qr.Bitmap()
	rows := len(matrix)
	if rows == 0 {
		return nil
	}
	cols := len(matrix[0])

	white := lipgloss.Color("#FFFFFF")
	black := lipgloss.Color("#000000")

	out := make([]string, 0, (rows+1)/2)
	for y := 0; y < rows; y += 2 {
		var b strings.Builder
		for x := 0; x < cols; x++ {
			top := matrix[y][x]
			var bottom bool
			if y+1 < rows {
				bottom = matrix[y+1][x]
			}
			topColor := white
			if top {
				topColor = black
			}
			bottomColor := white
			if bottom {
				bottomColor = black
			}
			st := lipgloss.NewStyle().Foreground(topColor).Background(bottomColor)
			b.WriteString(st.Render("▀"))
		}
		out = append(out, b.String())
	}
	return out
}

// qrModuleWidth returns how many terminal columns a QR for the given text
// will occupy (one cell per module + 0 padding). Returns 0 if the QR can't
// be encoded.
func qrModuleWidth(text string) int {
	if text == "" {
		return 0
	}
	qr, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return 0
	}
	m := qr.Bitmap()
	if len(m) == 0 {
		return 0
	}
	return len(m[0])
}
