package main

import (
	"fmt"
	"sync"
)

type RoomMessageType string

const (
	RoomMessageOffer      RoomMessageType = "offer"
	RoomMessageSubscribe  RoomMessageType = "subscribe_request"
	RoomMessageUpdate     RoomMessageType = "update_request"
	RoomMessageTrackEvent RoomMessageType = "track_event"
)

type RoomMessage struct {
	Type         RoomMessageType `json:"type"`
	RoomID       string          `json:"room_id"`
	FromPID      string          `json:"from_pid"`
	ToPID        string          `json:"to_pid"`
	AdjustmentID int             `json:"adjustment_id"`
	ItemIndex    int             `json:"item_index"`
	TrackID      string          `json:"track_id"`
	TrackGen     int             `json:"track_generation"`
	Params       MediaParams     `json:"params,omitempty"`
	Text         string          `json:"text,omitempty"`
}

// RoomBus 按完整参与身份（pid）注册和排队。同名重入会注册新的 pid，
// 因此旧连接排队的迟到消息永远不会投递到新连接。
type RoomBus struct {
	mu       sync.Mutex
	online   map[string]bool
	queues   map[string][]RoomMessage
	handlers map[string]func(RoomMessage)
}

func NewRoomBus() *RoomBus {
	return &RoomBus{
		online:   map[string]bool{},
		queues:   map[string][]RoomMessage{},
		handlers: map[string]func(RoomMessage){},
	}
}

func (b *RoomBus) Register(pid string, handler func(RoomMessage)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[pid] = handler
}

func (b *RoomBus) Connect(pid string) []RoomMessage {
	b.mu.Lock()
	b.online[pid] = true
	messages := b.queues[pid]
	b.queues[pid] = nil
	handler := b.handlers[pid]
	b.mu.Unlock()
	if handler != nil {
		for _, message := range messages {
			handler(message)
		}
	}
	return messages
}

func (b *RoomBus) Disconnect(pid string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.online[pid] = false
}

func (b *RoomBus) Send(message RoomMessage) error {
	b.mu.Lock()
	handler, ok := b.handlers[message.ToPID]
	if !ok {
		b.mu.Unlock()
		return fmt.Errorf("unknown participation endpoint %s", message.ToPID)
	}
	if !b.online[message.ToPID] {
		b.queues[message.ToPID] = append(b.queues[message.ToPID], message)
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	handler(message)
	return nil
}

func (b *RoomBus) Queued(pid string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queues[pid])
}
