package main

import (
	"fmt"
	"strings"
)

type MediaFrame struct {
	TrackID   string
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
		parts = append(parts, fmt.Sprintf("%s(%s,%s,%dbps,r%d,pub=%s,sub=%v)", track.ID, track.Kind, track.Codec, track.BitrateBps, track.Revision, track.Publisher, track.Subscribers))
	}
	return strings.Join(parts, ", ")
}

func (m *LocalMediaSource) ProduceFor(peerID string, tracks []TrackState) []MediaFrame {
	visible := make([]TrackState, 0, len(tracks))
	for _, track := range tracks {
		if track.Publisher == peerID || containsString(track.Subscribers, peerID) {
			visible = append(visible, track)
		}
	}
	return m.Produce(visible)
}
