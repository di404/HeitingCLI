package bili

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/skip2/go-qrcode"
)

// QRStatus is the state of a login QR code.
type QRStatus int

const (
	QRConfirmed  QRStatus = 0
	QRExpired    QRStatus = 86038
	QRScanned    QRStatus = 86090
	QRNotScanned QRStatus = 86101
)

// QRCode is a login code ready to be displayed.
type QRCode struct {
	Text string
	Key  string
}

// qrQuietModules is the blank margin around a terminal QR code. The standard's
// four modules would cost four rows, more than a typical window can spare;
// two still scan.
const qrQuietModules = 2

// renderQR draws two modules per character cell with half blocks. A cell's
// glyph is painted in the terminal's foreground, so on a dark terminal the
// painted parts are the code's light modules and the background is its dark
// ones.
func renderQR(modules [][]bool, darkTerminal bool) string {
	size := len(modules)
	painted := func(x, y int) bool {
		dark := x >= 0 && y >= 0 && x < size && y < size && modules[y][x]
		return dark != darkTerminal
	}
	var out strings.Builder
	for y := -qrQuietModules; y < size+qrQuietModules; y += 2 {
		for x := -qrQuietModules; x < size+qrQuietModules; x++ {
			switch top, bottom := painted(x, y), painted(x, y+1); {
			case top && bottom:
				out.WriteRune('█')
			case top:
				out.WriteRune('▀')
			case bottom:
				out.WriteRune('▄')
			default:
				out.WriteByte(' ')
			}
		}
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

// GenerateQR requests a login code, renders it for the terminal, and writes
// the same code to pngPath for terminals too small to show it.
func (c *Client) GenerateQR(ctx context.Context, darkTerminal bool, pngPath string) (*QRCode, error) {
	var data struct {
		URL string `json:"url"`
		Key string `json:"qrcode_key"`
	}
	if err := c.getJSON(ctx, "https://passport.bilibili.com/x/passport-login/web/qrcode/generate", &data); err != nil {
		return nil, err
	}
	if err := qrcode.WriteFile(data.URL, qrcode.Low, 256, pngPath); err != nil {
		return nil, err
	}
	// A separate code, because the library caches its first encoding and
	// would ignore DisableBorder on one that was already written out.
	code, err := qrcode.New(data.URL, qrcode.Low)
	if err != nil {
		return nil, err
	}
	code.DisableBorder = true
	return &QRCode{Text: renderQR(code.Bitmap(), darkTerminal), Key: data.Key}, nil
}

// PollQR checks a login code. When it returns QRConfirmed, cookies holds the
// new login in "name=value; name=value" form.
func (c *Client) PollQR(ctx context.Context, key string) (status QRStatus, cookies string, err error) {
	env, resp, err := c.fetch(ctx, fmt.Sprintf(
		"https://passport.bilibili.com/x/passport-login/web/qrcode/poll?qrcode_key=%s", url.QueryEscape(key)))
	if err != nil {
		return 0, "", err
	}
	if err := env.err(); err != nil {
		return 0, "", err
	}
	var data struct {
		Code QRStatus `json:"code"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return 0, "", err
	}
	if data.Code == QRConfirmed {
		var parts []string
		for _, cookie := range resp.Cookies() {
			parts = append(parts, cookie.Name+"="+cookie.Value)
		}
		cookies = strings.Join(parts, "; ")
	}
	return data.Code, cookies, nil
}
