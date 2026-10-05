package main

import (
	"fmt"
	"sync"
)

type Handler func(Message)

type Bus struct {
	mu       sync.Mutex
	online   map[string]bool
	queues   map[string][]Message
	handlers map[string]Handler
}

func NewBus() *Bus {
	return &Bus{
		online:   map[string]bool{},
		queues:   map[string][]Message{},
		handlers: map[string]Handler{},
	}
}

func (b *Bus) Register(peerID string, handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[peerID] = handler
}

// Unregister removes a participation identity and drops anything still
// queued for it. A same-name re-entry registers a new identity, so queued
// messages addressed to the old generation must never reach the new one.
func (b *Bus) Unregister(peerID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.handlers, peerID)
	delete(b.queues, peerID)
	delete(b.online, peerID)
}

func (b *Bus) Connect(peerID string) []Message {
	b.mu.Lock()
	b.online[peerID] = true
	messages := b.queues[peerID]
	b.queues[peerID] = nil
	handler := b.handlers[peerID]
	b.mu.Unlock()
	if handler != nil {
		for _, message := range messages {
			handler(message)
		}
	}
	return messages
}

func (b *Bus) Disconnect(peerID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.online[peerID] = false
}

func (b *Bus) Send(message Message) error {
	b.mu.Lock()
	if _, ok := b.handlers[message.To]; !ok {
		b.mu.Unlock()
		return fmt.Errorf("unknown endpoint %s", message.To)
	}
	if !b.online[message.To] {
		b.queues[message.To] = append(b.queues[message.To], message)
		b.mu.Unlock()
		return nil
	}
	handler := b.handlers[message.To]
	b.mu.Unlock()
	handler(message)
	return nil
}

func (b *Bus) Queued(peerID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queues[peerID])
}
