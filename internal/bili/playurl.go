package bili

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/di404/HeitingCLI/internal/audio"
)

type streamCandidate struct {
	url    string
	format string
	codec  string
	qn     int
}

var formatRank = map[string]int{"flv": 0, "ts": 1, "fmp4": 2}

type playInfo struct {
	LiveStatus  int `json:"live_status"`
	PlayurlInfo *struct {
		Playurl *struct {
			Stream []struct {
				Format []struct {
					FormatName string `json:"format_name"`
					Codec      []struct {
						CodecName string `json:"codec_name"`
						CurrentQn int    `json:"current_qn"`
						BaseURL   string `json:"base_url"`
						URLInfo   []struct {
							Host  string `json:"host"`
							Extra string `json:"extra"`
						} `json:"url_info"`
					} `json:"codec"`
				} `json:"format"`
			} `json:"stream"`
		} `json:"playurl"`
	} `json:"playurl_info"`
}

func (p *playInfo) candidates() []streamCandidate {
	if p.PlayurlInfo == nil || p.PlayurlInfo.Playurl == nil {
		return nil
	}
	var out []streamCandidate
	for _, stream := range p.PlayurlInfo.Playurl.Stream {
		for _, format := range stream.Format {
			for _, codec := range format.Codec {
				if host, extra, ok := pickHost(codec.URLInfo); ok {
					out = append(out, streamCandidate{
						url:    host + codec.BaseURL + extra,
						format: format.FormatName,
						codec:  codec.CodecName,
						qn:     codec.CurrentQn,
					})
				}
			}
		}
	}
	return out
}

// pickHost prefers CDN nodes over the unreliable peer-assisted "mcdn" hosts.
func pickHost(hosts []struct {
	Host  string `json:"host"`
	Extra string `json:"extra"`
}) (string, string, bool) {
	if len(hosts) == 0 {
		return "", "", false
	}
	for _, h := range hosts {
		if !strings.Contains(h.Host, "mcdn") {
			return h.Host, h.Extra, true
		}
	}
	return hosts[0].Host, hosts[0].Extra, true
}

// chooseStream picks the cheapest stream: the server has no audio-only
// variant, so the lowest video bitrate wastes the least bandwidth. FLV is one
// long connection with the lowest delay, so it wins over HLS.
func chooseStream(candidates []streamCandidate) (streamCandidate, bool) {
	if len(candidates) == 0 {
		return streamCandidate{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		ra, oka := formatRank[a.format]
		rb, okb := formatRank[b.format]
		if !oka {
			ra = len(formatRank)
		}
		if !okb {
			rb = len(formatRank)
		}
		if ra != rb {
			return ra < rb
		}
		if a.qn != b.qn {
			return a.qn < b.qn
		}
		return a.codec == "avc" && b.codec != "avc"
	})
	return candidates[0], true
}

// ResolveAudio finds a playable stream for a room. If the API gives no usable
// stream, it falls back to mpv's yt-dlp integration on the room page.
func (c *Client) ResolveAudio(ctx context.Context, roomID uint64) (audio.Source, error) {
	info := new(playInfo)
	err := c.getJSON(ctx, fmt.Sprintf(
		"https://api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo?room_id=%d&protocol=0,1&format=0,1,2&codec=0,1&qn=80&platform=web&ptype=8&dolby=5&panorama=1",
		roomID), info)
	if err == nil && info.LiveStatus != 1 {
		return audio.Source{}, audio.ErrOffline
	}
	if err != nil && ctx.Err() != nil {
		return audio.Source{}, ctx.Err()
	}
	var apiErr *APIError
	if err != nil && !errors.As(err, &apiErr) {
		return audio.Source{}, err
	}
	if err == nil {
		if stream, ok := chooseStream(info.candidates()); ok {
			return audio.Source{URL: stream.url, UserAgent: c.UserAgent(), Referer: liveReferer}, nil
		}
	}
	slog.Warn("no stream from play API, using yt-dlp", "room", roomID, "err", err)
	return audio.Source{URL: fmt.Sprintf("https://live.bilibili.com/%d", roomID), UseYTDL: true}, nil
}
