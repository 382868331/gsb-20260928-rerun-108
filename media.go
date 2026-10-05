package main

import (
	"fmt"
	"strings"
)

type MediaFrame struct {
	TrackID   string
	SourceID  string
	Owner     string
	Kind      string
	Sequence  int
	Timestamp int64
	Payload   []byte
}

type LocalMediaSource struct {
	sequence int
}

func (m *LocalMediaSource) Produce(tracks []TrackState) []MediaFrame {
	frames := make([]MediaFrame, 0, len(tracks))
	for _, track := range tracks {
		m.sequence++
		payload := make([]byte, 64)
		for i := range payload {
			payload[i] = byte(m.sequence + i)
		}
		frames = append(frames, MediaFrame{
			TrackID:   track.ID,
			SourceID:  track.SourceID,
			Owner:     track.Owner,
			Kind:      track.Kind,
			Sequence:  m.sequence,
			Timestamp: int64(m.sequence) * 20,
			Payload:   payload,
		})
	}
	return frames
}

func describeTracks(tracks []TrackState) string {
	if len(tracks) == 0 {
		return "无活动轨道，对应状态已释放"
	}
	parts := make([]string, 0, len(tracks))
	for _, track := range tracks {
		subscribers := append([]string(nil), track.Subscribers...)
		parts = append(parts, fmt.Sprintf("%s[src=%s,owner=%s](%s,%s,%dbps,sub=%s)", track.ID, track.SourceID, track.Owner, track.Kind, track.Codec, track.BitrateBps, strings.Join(subscribers, "+")))
	}
	return strings.Join(parts, ", ")
}
