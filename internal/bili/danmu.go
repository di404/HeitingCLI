package bili

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Medal is the fan badge shown beside a name.
type Medal struct {
	Level     int
	ShipLevel int
	Name      string
	Color     string
}

// Danmu is one chat message. Empty colors mean the terminal default.
type Danmu struct {
	UID          uint64
	Name         string
	Time         time.Time
	Content      string
	Medal        *Medal
	NameColor    string
	ContentColor string
}

// parseDanmu reads the positional DANMU_MSG payload. Its layout is undocumented
// and varies, so a malformed message is skipped rather than crashing the app.
func parseDanmu(info []any) (danmu *Danmu) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("malformed danmaku", "err", r)
			danmu = nil
		}
	}()
	basic := info[0].([]any)
	user := info[2].([]any)
	rawMedal := info[3].([]any)

	var medal *Medal
	if len(rawMedal) > 10 {
		medal = &Medal{
			Level:     int(rawMedal[0].(float64)),
			Name:      rawMedal[1].(string),
			Color:     fmt.Sprintf("#%06X", int64(rawMedal[4].(float64))),
			ShipLevel: int(rawMedal[10].(float64)),
		}
	}
	contentColor := fmt.Sprintf("#%06X", int64(basic[3].(float64)))
	if strings.EqualFold(contentColor, "#FFFFFF") {
		contentColor = ""
	}
	return &Danmu{
		UID:          uint64(user[0].(float64)),
		Name:         user[1].(string),
		Time:         time.UnixMilli(int64(basic[4].(float64))),
		Content:      info[1].(string),
		Medal:        medal,
		NameColor:    user[7].(string),
		ContentColor: contentColor,
	}
}
