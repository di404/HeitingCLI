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

// GenerateQR requests a login code, renders it for the terminal, and writes
// the same code to pngPath for terminals too small to show it.
func (c *Client) GenerateQR(ctx context.Context, inverse bool, pngPath string) (*QRCode, error) {
	var data struct {
		URL string `json:"url"`
		Key string `json:"qrcode_key"`
	}
	if err := c.getJSON(ctx, "https://passport.bilibili.com/x/passport-login/web/qrcode/generate", &data); err != nil {
		return nil, err
	}
	code, err := qrcode.New(data.URL, qrcode.Low)
	if err != nil {
		return nil, err
	}
	if err := code.WriteFile(256, pngPath); err != nil {
		return nil, err
	}
	return &QRCode{Text: code.ToSmallString(inverse), Key: data.Key}, nil
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
