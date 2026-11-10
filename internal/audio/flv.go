package audio

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	flvTagAudio    = 8
	flvCodecAAC    = 10
	aacPacketASC   = 0
	aacPacketRaw   = 1
	maxAudioTag    = 1 << 16
	flvHeaderSize  = 9
	flvTagHeadSize = 11
)

var errStreamEnded = errors.New("音频流已结束")

// flvSource extracts AAC from an FLV stream. Read serves the access units in
// the length-prefixed framing go-aac expects for raw streams, and reports
// io.EOF when the AudioSpecificConfig changes so the caller can build a new
// decoder from nextConfig. Tags other than audio are skipped, so a stream that
// still carries video plays too.
type flvSource struct {
	r       *bufio.Reader
	payload []byte
	frame   []byte
	config  []byte
	pending []byte
}

func newFLVSource(r io.Reader) (*flvSource, error) {
	br := bufio.NewReaderSize(r, 1<<14)
	var header [flvHeaderSize]byte
	if _, err := io.ReadFull(br, header[:]); err != nil {
		return nil, fmt.Errorf("读取 FLV 头: %w", err)
	}
	if !bytes.Equal(header[:3], []byte("FLV")) {
		return nil, errors.New("不是 FLV 数据")
	}
	offset := binary.BigEndian.Uint32(header[5:])
	if offset < flvHeaderSize {
		return nil, errors.New("FLV 头损坏")
	}
	if _, err := br.Discard(int(offset) - flvHeaderSize + 4); err != nil {
		return nil, fmt.Errorf("读取 FLV 头: %w", err)
	}
	return &flvSource{r: br}, nil
}

// nextPacket returns the next AAC packet's type and data.
func (s *flvSource) nextPacket() (byte, []byte, error) {
	var head [flvTagHeadSize]byte
	for {
		if _, err := io.ReadFull(s.r, head[:]); err != nil {
			return 0, nil, err
		}
		size := int(head[1])<<16 | int(head[2])<<8 | int(head[3])
		if head[0]&0x1f != flvTagAudio {
			if _, err := s.r.Discard(size + 4); err != nil {
				return 0, nil, err
			}
			continue
		}
		if size > maxAudioTag {
			return 0, nil, fmt.Errorf("音频帧过大: %d", size)
		}
		s.payload = s.payload[:0]
		s.payload = append(s.payload, make([]byte, size+4)...)
		if _, err := io.ReadFull(s.r, s.payload); err != nil {
			return 0, nil, err
		}
		tag := s.payload[:size]
		if len(tag) == 0 {
			continue
		}
		if codec := tag[0] >> 4; codec != flvCodecAAC {
			return 0, nil, fatalError{msg: "不支持的音频格式", detail: fmt.Sprintf("flv sound format %d", codec)}
		}
		if len(tag) >= 2 {
			return tag[1], tag[2:], nil
		}
	}
}

// nextConfig returns the stream's next AudioSpecificConfig.
func (s *flvSource) nextConfig() ([]byte, error) {
	if s.pending != nil {
		s.config, s.pending = s.pending, nil
		return s.config, nil
	}
	for {
		kind, data, err := s.nextPacket()
		if err != nil {
			return nil, endOfStream(err)
		}
		if kind == aacPacketASC && len(data) >= 2 {
			s.config = bytes.Clone(data)
			return s.config, nil
		}
	}
}

func (s *flvSource) Read(b []byte) (int, error) {
	for len(s.frame) == 0 {
		if s.pending != nil {
			return 0, io.EOF
		}
		kind, data, err := s.nextPacket()
		if err != nil {
			return 0, endOfStream(err)
		}
		switch {
		case kind == aacPacketASC && len(data) >= 2 && !bytes.Equal(data, s.config):
			s.pending = bytes.Clone(data)
			return 0, io.EOF
		case kind == aacPacketRaw && len(data) > 0 && len(data) <= 0xffff:
			s.frame = append(s.frame[:0], byte(len(data)>>8), byte(len(data)))
			s.frame = append(s.frame, data...)
		}
	}
	n := copy(b, s.frame)
	s.frame = s.frame[n:]
	return n, nil
}

func endOfStream(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errStreamEnded
	}
	return err
}
