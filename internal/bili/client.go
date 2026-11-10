// Package bili talks to the Bilibili live APIs and danmaku servers.
package bili

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultUserAgent is sent when the user has not configured one.
const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"

const liveReferer = "https://live.bilibili.com/"

var cookieURL = &url.URL{Scheme: "https", Host: "bilibili.com", Path: "/"}

// APIError is a non-zero "code" returned by a Bilibili API.
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s (%d)", e.Message, e.Code)
}

// Client is an HTTP client carrying the session cookies and User-Agent.
type Client struct {
	http *http.Client
	jar  http.CookieJar
	ua   atomic.Value
	wbi  wbiKeys
}

// New creates a client, restoring a saved login cookie if there is one.
func New(userAgent, cookies string) *Client {
	jar, _ := cookiejar.New(nil)
	c := &Client{jar: jar}
	c.SetUserAgent(userAgent)
	c.http = &http.Client{
		Jar:       jar,
		Timeout:   15 * time.Second,
		Transport: headerTransport{c},
	}
	c.SetCookies(cookies)
	return c
}

// SetUserAgent changes the User-Agent; empty selects the default.
func (c *Client) SetUserAgent(ua string) {
	if ua == "" {
		ua = DefaultUserAgent
	}
	c.ua.Store(ua)
}

// UserAgent returns the User-Agent in effect.
func (c *Client) UserAgent() string {
	return c.ua.Load().(string)
}

// SetCookies adds cookies from a "name=value; name=value" string.
func (c *Client) SetCookies(raw string) {
	var cookies []*http.Cookie
	for _, part := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && name != "" {
			cookies = append(cookies, newCookie(name, value))
		}
	}
	c.jar.SetCookies(cookieURL, cookies)
}

// ClearLogin drops every cookie, returning the client to guest state.
func (c *Client) ClearLogin() {
	var expired []*http.Cookie
	for _, old := range c.jar.Cookies(cookieURL) {
		cookie := newCookie(old.Name, "")
		cookie.MaxAge = -1
		expired = append(expired, cookie)
	}
	c.jar.SetCookies(cookieURL, expired)
}

func newCookie(name, value string) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", Domain: ".bilibili.com"}
}

func (c *Client) cookie(name string) string {
	for _, cookie := range c.jar.Cookies(cookieURL) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

type headerTransport struct{ c *Client }

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", t.c.UserAgent())
	if req.Header.Get("Referer") == "" {
		req.Header.Set("Referer", liveReferer)
	}
	return http.DefaultTransport.RoundTrip(req)
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

func (e *envelope) err() error {
	if e.Code == 0 {
		return nil
	}
	message := e.Message
	if message == "" {
		message = e.Msg
	}
	return &APIError{Code: e.Code, Message: message}
}

func (c *Client) do(req *http.Request) (*envelope, *http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	env := new(envelope)
	if err := json.Unmarshal(body, env); err != nil {
		return nil, nil, fmt.Errorf("响应解析失败: %w", err)
	}
	return env, resp, nil
}

func (c *Client) fetch(ctx context.Context, rawURL string) (*envelope, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	return c.do(req)
}

// getJSON fetches rawURL and decodes a successful response's data into out.
func (c *Client) getJSON(ctx context.Context, rawURL string, out any) error {
	env, _, err := c.fetch(ctx, rawURL)
	if err != nil {
		return err
	}
	if err := env.err(); err != nil {
		return err
	}
	return json.Unmarshal(env.Data, out)
}
