package bili

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

var roomPattern = regexp.MustCompile(`^(?:https?://live\.bilibili\.com/(?:blanc/|h5/)?)?([0-9]+)(?:[/?#].*)?$`)

// ParseRoomID accepts a room number or a live.bilibili.com link.
func ParseRoomID(input string) (uint64, bool) {
	match := roomPattern.FindStringSubmatch(input)
	if match == nil {
		return 0, false
	}
	id, err := strconv.ParseUint(match[1], 10, 64)
	return id, err == nil && id != 0
}

// RoomInfo describes a live room.
type RoomInfo struct {
	RoomID     uint64 `json:"room_id"`
	ShortID    uint64 `json:"short_id"`
	UID        uint64 `json:"uid"`
	Title      string `json:"title"`
	LiveStatus int    `json:"live_status"`
}

// IsLive reports whether the room is broadcasting.
func (r RoomInfo) IsLive() bool { return r.LiveStatus == 1 }

// GetRoomInfo resolves a room number, including short ones.
func (c *Client) GetRoomInfo(ctx context.Context, roomID uint64) (*RoomInfo, error) {
	info := new(RoomInfo)
	err := c.getJSON(ctx, fmt.Sprintf("https://api.live.bilibili.com/room/v1/Room/get_info?room_id=%d", roomID), info)
	if err != nil {
		return nil, err
	}
	return info, nil
}

// GetAnchorName returns the streamer's display name.
func (c *Client) GetAnchorName(ctx context.Context, roomID uint64) (string, error) {
	var data struct {
		Info struct {
			Uname string `json:"uname"`
		} `json:"info"`
	}
	err := c.getJSON(ctx, fmt.Sprintf("https://api.live.bilibili.com/live_user/v1/UserInfo/get_anchor_in_room?roomid=%d", roomID), &data)
	return data.Info.Uname, err
}

// User is the logged-in account.
type User struct {
	UID  uint64
	Name string
}

type navData struct {
	IsLogin bool   `json:"isLogin"`
	Mid     uint64 `json:"mid"`
	Uname   string `json:"uname"`
	WbiImg  struct {
		ImgURL string `json:"img_url"`
		SubURL string `json:"sub_url"`
	} `json:"wbi_img"`
}

// nav returns the account info. A guest (-101) still receives the WBI keys.
func (c *Client) nav(ctx context.Context) (*navData, int, error) {
	env, _, err := c.fetch(ctx, "https://api.bilibili.com/x/web-interface/nav")
	if err != nil {
		return nil, 0, err
	}
	if env.Code != 0 && env.Code != -101 {
		return nil, env.Code, env.err()
	}
	data := new(navData)
	if err := json.Unmarshal(env.Data, data); err != nil {
		return nil, env.Code, err
	}
	return data, env.Code, nil
}

// CurrentUser returns the logged-in user, or nil for a guest.
func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	data, code, err := c.nav(ctx)
	if err != nil {
		return nil, err
	}
	if code != 0 || !data.IsLogin {
		return nil, nil
	}
	return &User{UID: data.Mid, Name: data.Uname}, nil
}

// ensureBuvid obtains the device cookies the danmaku APIs now require.
func (c *Client) ensureBuvid(ctx context.Context) error {
	if c.cookie("buvid3") != "" {
		return nil
	}
	var data struct {
		B3 string `json:"b_3"`
		B4 string `json:"b_4"`
	}
	if err := c.getJSON(ctx, "https://api.bilibili.com/x/frontend/finger/spi", &data); err != nil {
		return err
	}
	c.jar.SetCookies(cookieURL, []*http.Cookie{newCookie("buvid3", data.B3), newCookie("buvid4", data.B4)})
	return nil
}

// DanmuProperty holds the account's danmaku settings for a room.
type DanmuProperty struct {
	Bubble int `json:"bubble"`
	Danmu  struct {
		Mode   int `json:"mode"`
		Color  int `json:"color"`
		Length int `json:"length"`
	} `json:"danmu"`
}

func (c *Client) getDanmuProperty(ctx context.Context, roomID uint64) (*DanmuProperty, error) {
	var data struct {
		Property DanmuProperty `json:"property"`
	}
	err := c.getJSON(ctx, fmt.Sprintf("https://api.live.bilibili.com/xlive/web-room/v1/index/getInfoByUser?room_id=%d", roomID), &data)
	if err != nil {
		return nil, err
	}
	return &data.Property, nil
}

// SendDanmu posts a message to a room.
func (c *Client) SendDanmu(ctx context.Context, roomID uint64, text string, prop *DanmuProperty) error {
	csrf := c.cookie("bili_jct")
	if csrf == "" {
		return errors.New("未登录")
	}
	fields := map[string]string{
		"bubble":     "0",
		"msg":        text,
		"color":      "16777215",
		"mode":       "1",
		"fontsize":   "25",
		"rnd":        strconv.FormatInt(time.Now().Unix(), 10),
		"roomid":     strconv.FormatUint(roomID, 10),
		"csrf":       csrf,
		"csrf_token": csrf,
	}
	if prop != nil {
		fields["bubble"] = strconv.Itoa(prop.Bubble)
		fields["color"] = strconv.Itoa(prop.Danmu.Color)
		fields["mode"] = strconv.Itoa(prop.Danmu.Mode)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := form.WriteField(key, value); err != nil {
			return err
		}
	}
	form.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.live.bilibili.com/msg/send", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	env, _, err := c.do(req)
	if err != nil {
		return err
	}
	if err := env.err(); err != nil {
		return err
	}
	// A filtered message is reported through "message" with code 0.
	if env.Message == "f" || env.Message == "k" {
		return errors.New("弹幕被屏蔽")
	}
	return nil
}

func (c *Client) sendRoomHeartbeat(ctx context.Context, roomID uint64, interval int) (int, error) {
	hb := encodeHeartbeat(interval, roomID)
	values := url.Values{"hb": {hb}, "pf": {"web"}}
	var data struct {
		NextInterval int `json:"next_interval"`
	}
	err := c.getJSON(ctx, "https://live-trace.bilibili.com/xlive/rdata-interface/v1/heartbeat/webHeartBeat?"+values.Encode(), &data)
	if err != nil {
		return interval, err
	}
	if data.NextInterval <= 0 {
		return interval, nil
	}
	return data.NextInterval, nil
}
