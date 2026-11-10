package bili

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/di404/HeitingCLI/internal/audio"
)

type playInfo struct {
	LiveStatus  int `json:"live_status"`
	PlayurlInfo *struct {
		Playurl *struct {
			Stream []struct {
				ProtocolName string `json:"protocol_name"`
				Format       []struct {
					FormatName string `json:"format_name"`
					Codec      []struct {
						BaseURL   string `json:"base_url"`
						IsPushing *bool  `json:"is_pushing"`
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

// flvURLs lists the stream's FLV addresses, named CDN nodes first: bare IPs
// and peer-assisted "mcdn" hosts are less reliable. only_audio has no effect
// on HLS, so only FLV is usable.
func (p *playInfo) flvURLs() []string {
	if p.PlayurlInfo == nil || p.PlayurlInfo.Playurl == nil {
		return nil
	}
	var urls []string
	for _, stream := range p.PlayurlInfo.Playurl.Stream {
		if stream.ProtocolName != "http_stream" {
			continue
		}
		for _, format := range stream.Format {
			if format.FormatName != "flv" {
				continue
			}
			for _, codec := range format.Codec {
				if codec.IsPushing != nil && !*codec.IsPushing {
					continue
				}
				for _, info := range codec.URLInfo {
					urls = append(urls, info.Host+codec.BaseURL+info.Extra)
				}
			}
		}
	}
	sort.SliceStable(urls, func(i, j int) bool {
		return !unreliableHost(urls[i]) && unreliableHost(urls[j])
	})
	return urls
}

func unreliableHost(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return true
	}
	return net.ParseIP(parsed.Hostname()) != nil || strings.Contains(parsed.Hostname(), "mcdn")
}

// ResolveAudio finds the room's audio-only stream. With only_audio=1 the FLV
// stream carries no video, although its URL and metadata look unchanged. Rooms
// without transcoding only list their stream under ptype=1.
func (c *Client) ResolveAudio(ctx context.Context, roomID uint64) (audio.Source, error) {
	for _, ptype := range []int{8, 1} {
		info := new(playInfo)
		err := c.getJSON(ctx, fmt.Sprintf(
			"https://api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo?room_id=%d&protocol=0,1&format=0,1,2&codec=0,1&qn=10000&platform=web&ptype=%d&only_audio=1",
			roomID, ptype), info)
		if err != nil {
			return audio.Source{}, err
		}
		if info.LiveStatus != 1 {
			return audio.Source{}, audio.ErrOffline
		}
		if urls := info.flvURLs(); len(urls) > 0 {
			return audio.Source{URLs: urls, UserAgent: c.UserAgent(), Referer: liveReferer}, nil
		}
	}
	return audio.Source{}, audio.ErrNoStream
}
