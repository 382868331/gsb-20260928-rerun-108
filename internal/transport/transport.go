// Package transport 提供不依赖任何通信平台的进程内可控管道，
// 用于演示交叉消息、乱序、断开与恢复。
package transport

import (
	"sync"
)

type Envelope struct {
	From string
	To   string
	Seq  int64
	Body any
}

type handler func(Envelope)

type Link struct {
	mu     sync.Mutex
	buffer int
	peers  map[string]*peer
	drop   func(Envelope) bool // 注入丢包/延迟乱序策略
}

type peer struct {
	id      string
	inbox   chan Envelope
	h       handler
	offline bool
	queued  []Envelope // 断线期间缓存
	outbox  []Envelope // 断线期间尝试外发的消息
}

func NewLink(buffer int) *Link {
	return &Link{peers: map[string]*peer{}, buffer: buffer}
}

// Join 注册一个端点并开始收消息。
func (l *Link) Join(id string, h handler) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := &peer{id: id, inbox: make(chan Envelope, l.buffer), h: h}
	l.peers[id] = p
	go l.drain(p)
}

// Disconnect 把端点置为离线：新消息进入缓存，不投递。
func (l *Link) Disconnect(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p := l.peers[id]; p != nil {
		p.offline = true
	}
}

// Reconnect 恢复端点并按序投递断线期间缓存的消息，保证最终一致。
func (l *Link) Reconnect(id string) int {
	l.mu.Lock()
	p := l.peers[id]
	if p == nil {
		l.mu.Unlock()
		return 0
	}
	p.offline = false
	outbox := p.outbox
	p.outbox = nil
	l.mu.Unlock()
	for _, env := range outbox {
		l.Send(env)
	}
	l.mu.Lock()
	queued := p.queued
	p.queued = nil
	l.mu.Unlock()
	for _, env := range queued {
		p.inbox <- env
	}
	return len(outbox) + len(queued)
}

// Send 投递消息；离线时缓存，命中 drop 策略时丢弃。
func (l *Link) Send(env Envelope) {
	l.mu.Lock()
	if from := l.peers[env.From]; from != nil && from.offline {
		from.outbox = append(from.outbox, env)
		l.mu.Unlock()
		return
	}
	if l.drop != nil && l.drop(env) {
		l.mu.Unlock()
		return
	}
	p := l.peers[env.To]
	if p == nil {
		l.mu.Unlock()
		return
	}
	if p.offline {
		p.queued = append(p.queued, env)
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	p.inbox <- env
}

// Pending 返回某端点断线期间缓存的消息数。
func (l *Link) Pending(id string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p := l.peers[id]; p != nil {
		return len(p.queued)
	}
	return 0
}

func (l *Link) drain(p *peer) {
	for env := range p.inbox {
		p.h(env)
	}
}
