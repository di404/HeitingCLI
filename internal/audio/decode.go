package audio

import (
	"errors"
	"fmt"
	"io"

	"github.com/tphakala/go-aac/pcm"
)

// decodeStream decodes src until it ends, passing 48 kHz stereo s16le PCM to
// write. It only returns on an error.
func decodeStream(src *flvSource, write func([]byte)) error {
	buf := make([]byte, 16*1024)
	var converted []byte
	for {
		config, err := src.nextConfig()
		if err != nil {
			return err
		}
		decoder, err := pcm.NewDecoder(src, pcm.WithRawStream(config))
		if err != nil {
			return fatalError{msg: "不支持的音频格式", detail: fmt.Sprintf("config %x: %v", config, err)}
		}
		info := decoder.Info()
		if info.Channels < 1 || info.Channels > 2 {
			return fatalError{msg: "不支持的音频格式", detail: fmt.Sprintf("%d channels", info.Channels)}
		}
		convert := newConverter(info.SampleRate, info.Channels)
		for {
			n, err := decoder.Read(buf)
			if n > 0 {
				converted = convert.append(converted[:0], buf[:n])
				write(converted)
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
		}
	}
}
