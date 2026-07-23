package qrtext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderProducesSquareBorderedQRCode(t *testing.T) {
	rendered, err := Render("https://kigo.example/#c=K7M9Q2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, darkCell) {
		t.Fatal("rendered QR contains no dark modules")
	}
	lines := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
	if len(lines) < 20 {
		t.Fatalf("rendered QR has only %d rows", len(lines))
	}
	width := utf8.RuneCountInString(lines[0])
	if width != len(lines)*2 {
		t.Fatalf("rendered QR width=%d runes rows=%d", width, len(lines))
	}
	for index, line := range lines {
		if utf8.RuneCountInString(line) != width {
			t.Fatalf("row %d width=%d, want %d", index, utf8.RuneCountInString(line), width)
		}
	}
	if strings.TrimSpace(lines[0]) != "" || strings.TrimSpace(lines[len(lines)-1]) != "" {
		t.Fatal("rendered QR is missing its vertical quiet zone")
	}
}

func TestRenderRejectsEmptyText(t *testing.T) {
	if _, err := Render(""); err == nil {
		t.Fatal("empty QR text was accepted")
	}
}
