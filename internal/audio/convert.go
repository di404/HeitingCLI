package audio

import (
	"encoding/binary"
	"math"
)

const (
	outputRate           = 48000
	outputBytesPerFrame  = 4
	outputBytesPerSecond = outputRate * outputBytesPerFrame
)

// converter turns decoded s16le PCM into the output format. The audio device
// is opened once per process, so every stream has to match one fixed format.
type converter struct {
	channels int
	step     float64
	position float64
	frames   [][2]int16
}

func newConverter(rate, channels int) *converter {
	// One zero frame gives the interpolator a sample before the first one.
	return &converter{
		channels: channels,
		step:     float64(rate) / outputRate,
		position: 1,
		frames:   make([][2]int16, 1, 2048),
	}
}

func (c *converter) append(dst, src []byte) []byte {
	if c.channels == 2 && c.step == 1 {
		return append(dst, src...)
	}
	frameSize := 2 * c.channels
	for i := 0; i+frameSize <= len(src); i += frameSize {
		left := int16(binary.LittleEndian.Uint16(src[i:]))
		right := int16(binary.LittleEndian.Uint16(src[i+frameSize-2:]))
		c.frames = append(c.frames, [2]int16{left, right})
	}
	if c.step == 1 {
		for _, frame := range c.frames[1:] {
			dst = appendFrame(dst, frame[0], frame[1])
		}
		c.frames = c.frames[:1]
		return dst
	}
	for {
		index := int(c.position)
		if index+2 >= len(c.frames) {
			break
		}
		t := c.position - float64(index)
		a, b, d, e := c.frames[index-1], c.frames[index], c.frames[index+1], c.frames[index+2]
		dst = appendFrame(dst, hermite(a[0], b[0], d[0], e[0], t), hermite(a[1], b[1], d[1], e[1], t))
		c.position += c.step
	}
	consumed := int(c.position) - 1
	remaining := copy(c.frames, c.frames[consumed:])
	c.frames = c.frames[:remaining]
	c.position -= float64(consumed)
	return dst
}

func appendFrame(dst []byte, left, right int16) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, uint16(left))
	return binary.LittleEndian.AppendUint16(dst, uint16(right))
}

func hermite(a, b, c, d int16, t float64) int16 {
	p0, p1, p2, p3 := float64(a), float64(b), float64(c), float64(d)
	v := p1 + 0.5*t*(p2-p0+t*(2*p0-5*p1+4*p2-p3+t*(3*(p1-p2)+p3-p0)))
	return int16(math.Max(math.MinInt16, math.Min(math.MaxInt16, math.Round(v))))
}
