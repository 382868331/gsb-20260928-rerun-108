// Package coord 在协商内核之上提供一个最小的双方消息协调层。
// 它只负责转发 offer/answer，不绑定任何真实通信平台。
package coord

import (
	"sync"

	"avsession/internal/session"
	"avsession/internal/transport"
)

// Server 持有全部会话的权威协商状态。
type Server struct {
	mu      sync.Mutex
	engine  *session.Engine
	media   *session.MediaRuntime
	link    *transport.Link
	seqs    map[string]int64 // peer -> 已处理最大序号，额外防重
	onEvent func(Event)
}

// Event 是对外暴露的状态变更事件，便于演示观察最终一致。
type Event struct {
	Kind    string // committed | waiting | stale | failed
	Session string
	Peer    string
	Version int64
	Tracks  []session.ActiveTrack
	Added   []string
	Removed []string
	Reason  string
}

func NewServer(engine *session.Engine, media *session.MediaRuntime, link *transport.Link, onEvent func(Event)) *Server {
	return &Server{
		engine: engine, media: media, link: link,
		seqs: map[string]int64{}, onEvent: onEvent,
	}
}

// ConfigureRequest 是一端发起或答复配置的消息体。
type ConfigureRequest struct {
	Session string
	Base    int64
	Tracks  map[string]session.TrackSpec
}

// ConfigureReply 回到发起端的结果；Version 始终是服务器当前版本。
type ConfigureReply struct {
	Code    session.ResultCode
	Version int64
	Reason  string
}

// HandleConfigure 接收一端消息并驱动协商；成功后通知双方刷新媒体状态。
func (s *Server) HandleConfigure(env transport.Envelope) {
	req := env.Body.(ConfigureRequest)
	s.mu.Lock()
	seq := s.seqs[env.From]
	s.seqs[env.From] = seq + 1
	s.mu.Unlock()

	res, err := s.engine.Configure(session.Intent{
		SessionID: req.Session,
		PeerID:    env.From,
		Seq:       env.Seq,
		Base:      req.Base,
		Tracks:    req.Tracks,
	})

	reply := ConfigureReply{Code: res.Code, Version: res.Version}
	if err != nil {
		reply.Reason = err.Error()
		s.emit(Event{Kind: "failed", Session: req.Session, Peer: env.From, Reason: err.Error()})
	} else {
		switch res.Code {
		case session.ResultCommitted:
			s.media.Apply(req.Session, res.Tracks, nil)
			s.emit(Event{
				Kind: "committed", Session: req.Session, Peer: env.From, Version: res.Version,
				Tracks: res.Tracks, Added: res.Added, Removed: res.Removed,
			})
		case session.ResultWaiting:
			s.emit(Event{Kind: "waiting", Session: req.Session, Peer: env.From, Version: res.Version})
		case session.ResultStale:
			s.emit(Event{Kind: "stale", Session: req.Session, Peer: env.From, Version: res.Version})
		}
	}

	// 把答复发给请求方；断线期间由传输层缓存，恢复后送达。
	s.link.Send(transport.Envelope{From: "server", To: env.From, Seq: seq, Body: reply})
	if err == nil && res.Code == session.ResultCommitted {
		// 广播当前版本快照给双方，让两端最终收敛到同一份轨道状态。
		snap := SessionSnapshot{Version: res.Version, Tracks: res.Tracks}
		s.link.Send(transport.Envelope{From: "server", To: "alice", Seq: seq, Body: snap})
		s.link.Send(transport.Envelope{From: "server", To: "bob", Seq: seq, Body: snap})
	}
}

// SessionSnapshot 是服务器下发的权威会话快照。
type SessionSnapshot struct {
	Version int64
	Tracks  []session.ActiveTrack
}

func (s *Server) emit(e Event) {
	if s.onEvent != nil {
		s.onEvent(e)
	}
}
