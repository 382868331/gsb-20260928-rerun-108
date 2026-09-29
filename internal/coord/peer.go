package coord

import (
	"sync/atomic"

	"avsession/internal/session"
	"avsession/internal/transport"
)

// Peer 是一个端的本地视图：维护自己看到的最新版本，忽略更旧版本快照。
type Peer struct {
	id      string
	session string
	link    *transport.Link
	version atomic.Int64
	seq     atomic.Int64
	tracks  atomic.Pointer[[]session.ActiveTrack]
	onReply func(ConfigureReply)
}

func NewPeer(id, sess string, link *transport.Link, onReply func(ConfigureReply)) *Peer {
	p := &Peer{id: id, session: sess, link: link, onReply: onReply}
	link.Join(id, p.receive)
	return p
}

func (p *Peer) Version() int64 { return p.version.Load() }

func (p *Peer) Tracks() []session.ActiveTrack {
	t := p.tracks.Load()
	if t == nil {
		return nil
	}
	return *t
}

// Configure 基于本地已知版本提交配置（offer 或 answer 同构）。
func (p *Peer) Configure(tracks map[string]session.TrackSpec) {
	p.ConfigureAt(p.version.Load(), tracks)
}

// ConfigureAt 基于显式版本提交，用于演示迟到答复（base 落后于当前版本）。
func (p *Peer) ConfigureAt(base int64, tracks map[string]session.TrackSpec) {
	seq := p.seq.Add(1)
	p.link.Send(transport.Envelope{
		From: p.id, To: "server", Seq: seq,
		Body: ConfigureRequest{Session: p.session, Base: base, Tracks: tracks},
	})
}

func (p *Peer) receive(env transport.Envelope) {
	switch v := env.Body.(type) {
	case ConfigureReply:
		p.advance(v.Version)
		if p.onReply != nil {
			p.onReply(v)
		}
	case SessionSnapshot:
		// 迟到快照不能覆盖新版本。
		for {
			cur := p.version.Load()
			if v.Version < cur {
				return
			}
			if p.version.CompareAndSwap(cur, v.Version) {
				tracks := append([]session.ActiveTrack(nil), v.Tracks...)
				p.tracks.Store(&tracks)
				return
			}
		}
	}
}

func (p *Peer) advance(v int64) {
	for {
		cur := p.version.Load()
		if v <= cur {
			return
		}
		if p.version.CompareAndSwap(cur, v) {
			return
		}
	}
}
