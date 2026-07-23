package qrtext

import (
	"errors"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	quietZone = 1
	darkCell  = "██"
	lightCell = "  "
)

func Render(text string) (string, error) {
	if text == "" {
		return "", errors.New("QR text is empty")
	}
	code, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return "", err
	}
	code.DisableBorder = true
	bitmap := code.Bitmap()
	if len(bitmap) == 0 {
		return "", errors.New("QR bitmap is empty")
	}

	var out strings.Builder
	width := len(bitmap) + quietZone*2
	out.Grow(width * len(darkCell) * (len(bitmap) + quietZone*2 + 1))
	for y := -quietZone; y < len(bitmap)+quietZone; y++ {
		for x := -quietZone; x < len(bitmap)+quietZone; x++ {
			dark := y >= 0 && y < len(bitmap) &&
				x >= 0 && x < len(bitmap[y]) &&
				bitmap[y][x]
			if dark {
				out.WriteString(darkCell)
			} else {
				out.WriteString(lightCell)
			}
		}
		out.WriteByte('\n')
	}
	return out.String(), nil
}
