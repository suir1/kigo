package app

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/suir1/kigo/internal/note"
	"github.com/suir1/kigo/internal/qrtext"
)

func transferPublicLink(g *globalOptions, code string) string {
	if g == nil || strings.TrimSpace(g.WebURL) == "" {
		return ""
	}
	mode, _ := normalizeTransportMode(g.Transport)
	if mode == transportModeNative {
		return ""
	}
	return strings.TrimRight(g.WebURL, "/") + "/#c=" + code
}

func notePublicLink(g *globalOptions, code, pad string) string {
	if g == nil || strings.TrimSpace(g.WebURL) == "" {
		return ""
	}
	mode, _ := normalizeTransportMode(g.Transport)
	if mode == transportModeNative {
		return ""
	}
	values := url.Values{"n": []string{code}}
	pad = note.NormalizePad(pad)
	if pad != note.DefaultPad {
		values.Set("p", pad)
	}
	return strings.TrimRight(g.WebURL, "/") + "/#" + values.Encode()
}

func transferQRCodeTarget(g *globalOptions, code string) string {
	if link := transferPublicLink(g, code); link != "" {
		return link
	}
	return code
}

func noteQRCodeTarget(g *globalOptions, code, pad string) string {
	if link := notePublicLink(g, code, pad); link != "" {
		return link
	}
	return code
}

func printQRCodeIfTerminal(out io.Writer, text string, enabled bool) {
	if !enabled || !writerIsTerminal(out) {
		return
	}
	rendered, err := qrtext.Render(text)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not render QR code:", err)
		return
	}
	fmt.Fprintln(out, "QR:")
	fmt.Fprint(out, rendered)
}

func writerIsTerminal(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	fd := file.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}
