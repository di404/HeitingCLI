package bili

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

type wbiKeys struct {
	mu      sync.Mutex
	mixin   string
	updated time.Time
}

var mixinKeyEncTab = [...]int{
	46, 47, 18, 2, 53, 8, 23, 32,
	15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19,
	29, 28, 14, 39, 12, 38, 41, 13,
	37, 48, 7, 16, 24, 55, 40, 61,
	26, 17, 0, 1, 60, 51, 30, 4,
	22, 25, 54, 21, 56, 59, 6, 63,
	57, 62, 11, 36, 20, 34, 44, 52,
}

func mixinKey(img, sub string) string {
	raw := img + sub
	var mixin [32]byte
	for i := range mixin {
		mixin[i] = raw[mixinKeyEncTab[i]]
	}
	return string(mixin[:])
}

func wbiSign(values url.Values, mixin string, now time.Time) {
	values.Set("wts", strconv.FormatInt(now.Unix(), 10))
	hash := md5.Sum([]byte(values.Encode() + mixin))
	values.Set("w_rid", hex.EncodeToString(hash[:]))
}

// signedURL appends a WBI signature to base?values.
func (c *Client) signedURL(ctx context.Context, base string, values url.Values) (string, error) {
	mixin, err := c.wbiMixin(ctx)
	if err != nil {
		return "", fmt.Errorf("获取签名密钥失败: %w", err)
	}
	wbiSign(values, mixin, time.Now())
	return base + "?" + values.Encode(), nil
}

func (c *Client) wbiMixin(ctx context.Context) (string, error) {
	c.wbi.mu.Lock()
	defer c.wbi.mu.Unlock()
	if c.wbi.mixin != "" && time.Since(c.wbi.updated) < time.Hour {
		return c.wbi.mixin, nil
	}
	nav, _, err := c.nav(ctx)
	if err != nil {
		return "", err
	}
	img := strings.TrimSuffix(path.Base(nav.WbiImg.ImgURL), ".png")
	sub := strings.TrimSuffix(path.Base(nav.WbiImg.SubURL), ".png")
	if len(img)+len(sub) < 64 {
		return "", fmt.Errorf("unexpected wbi keys %q %q", img, sub)
	}
	c.wbi.mixin = mixinKey(img, sub)
	c.wbi.updated = time.Now()
	return c.wbi.mixin, nil
}
