package main

import "fmt"

// RoomMediaFrame 带来源世代，轨道 ID 被同名重入者复用时不会混淆来源。
type RoomMediaFrame struct {
	TrackID  string
	OwnerPID string
	OwnerGen int
	Receiver string
	Kind     string
	Sequence int
	Params   MediaParams
	Payload  []byte
}

type RoomMediaSource struct {
	sequence int
}

// Produce 只按某个参与身份概况中当前 active 的订阅产帧；已拒绝、待决或
// 撤回的订阅都不会产生媒体。
func (m *RoomMediaSource) Produce(view *Overview) []RoomMediaFrame {
	if view == nil {
		return nil
	}
	frames := make([]RoomMediaFrame, 0, len(view.Visible))
	for _, track := range view.Visible {
		m.sequence++
		payload := make([]byte, 32)
		for i := range payload {
			payload[i] = byte(m.sequence + i)
		}
		frames = append(frames, RoomMediaFrame{
			TrackID:  track.ID,
			OwnerPID: track.OwnerPID,
			OwnerGen: track.OwnerGeneration,
			Receiver: view.SelfPID,
			Kind:     track.Kind,
			Sequence: m.sequence,
			Params:   track.Params,
			Payload:  payload,
		})
	}
	return frames
}

func describeRoomFrames(frames []RoomMediaFrame) string {
	if len(frames) == 0 {
		return "无可接收轨道（无帧）"
	}
	parts := make([]string, 0, len(frames))
	for _, frame := range frames {
		parts = append(parts, fmt.Sprintf("%s#g%d/%s:%dbps#%d", frame.TrackID, frame.OwnerGen, frame.Params.Codec, frame.Params.BitrateBps, frame.Sequence))
	}
	return joinStrings(parts, ", ")
}

func joinStrings(items []string, sep string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for _, item := range items[1:] {
		out += sep + item
	}
	return out
}
