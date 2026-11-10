package bili

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/andybalholm/brotli"
)

func compress(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := brotli.NewWriter(&out)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestDecodeNestedBrotliFrames(t *testing.T) {
	first, second := []byte(`{"cmd":"LIVE"}`), []byte(`{"cmd":"PREPARING"}`)
	inner := append(packFrame(verPlain, opMessage, 0, first), packFrame(verPlain, opMessage, 0, second)...)
	outer := packFrame(verBrotli, opMessage, 0, compress(t, inner))

	parsed, err := readFrame(bytes.NewReader(outer))
	if err != nil {
		t.Fatal(err)
	}
	messages := decodeMessages(parsed)
	if len(messages) != 2 || !bytes.Equal(messages[0], first) || !bytes.Equal(messages[1], second) {
		t.Fatalf("got %q", messages)
	}
}

func TestReadFrameRejectsBadSizes(t *testing.T) {
	bad := packFrame(verPlain, opMessage, 0, nil)
	bad[3] = 4 // Total size smaller than the header.
	if _, err := readFrame(bytes.NewReader(bad)); err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseCommandDanmu(t *testing.T) {
	info := []any{
		[]any{0, 1, 25, 16777215, 1700000000000},
		"你好",
		[]any{42, "名字", 0, 0, 0, 0, 0, "#00D1F1"},
		[]any{21, "牌子", "主播", 1, 398668, "", 0, 0, 0, 0, 3, 0},
	}
	body, err := json.Marshal(map[string]any{"cmd": "DANMU_MSG:4:0:2:2:2:0", "info": info})
	if err != nil {
		t.Fatal(err)
	}
	event, ok := parseCommand(body).(EventDanmu)
	if !ok {
		t.Fatalf("not a danmaku: %#v", parseCommand(body))
	}
	d := event.Danmu
	if d.Name != "名字" || d.Content != "你好" || d.ContentColor != "" || d.Medal.ShipLevel != 3 || d.Medal.Color != "#06154C" {
		t.Fatalf("got %+v medal %+v", d, d.Medal)
	}
}

func TestParseCommandMalformedDanmu(t *testing.T) {
	if event := parseCommand([]byte(`{"cmd":"DANMU_MSG","info":[1]}`)); event != nil {
		t.Fatalf("got %#v", event)
	}
}

func TestParseRoomID(t *testing.T) {
	cases := map[string]uint64{
		"7777":                                  7777,
		"https://live.bilibili.com/22222":       22222,
		"https://live.bilibili.com/blanc/1?a=b": 1,
		"https://live.bilibili.com/h5/3":        3,
		"abc":                                   0,
		"https://example.com/123":               0,
		"0":                                     0,
	}
	for input, want := range cases {
		got, ok := ParseRoomID(input)
		if got != want || ok != (want != 0) {
			t.Errorf("ParseRoomID(%q) = %d, %v", input, got, ok)
		}
	}
}

func TestChooseStreamPrefersFLVLowestQuality(t *testing.T) {
	got, ok := chooseStream([]streamCandidate{
		{url: "hls", format: "ts", codec: "avc", qn: 80},
		{url: "high", format: "flv", codec: "avc", qn: 10000},
		{url: "hevc", format: "flv", codec: "hevc", qn: 250},
		{url: "best", format: "flv", codec: "avc", qn: 250},
	})
	if !ok || got.url != "best" {
		t.Fatalf("got %+v", got)
	}
}

func TestWbiKnownVector(t *testing.T) {
	// From the bilibili-API-collect WBI documentation.
	mixin := mixinKey("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")
	if mixin != "ea1db124af3c7062474693fa704f4ff8" {
		t.Fatalf("got %s", mixin)
	}
}
