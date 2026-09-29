package session

import (
	"fmt"
	"sync"
)

// Frame 是本地模拟端产生的媒体帧。真实传输在后续版本接入。
type Frame struct {
	TrackID string
	Sender  string
	Seq     uint64
	Bytes   int
}

// MediaRuntime 依据当前生效轨道管理模拟发送源；停用轨道即停止并释放。
type MediaRuntime struct {
	mu      sync.Mutex
	sources map[string]map[string]*source // sessionID -> trackID
}

func NewMediaRuntime() *MediaRuntime {
	return &MediaRuntime{sources: map[string]map[string]*source{}}
}

type source struct {
	stop chan struct{}
}

// Apply 按新版本轨道集合做差集：新增的起模拟源，消失的停止释放。
func (m *MediaRuntime) Apply(session string, tracks []ActiveTrack, onFrame func(Frame)) (added, removed []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sources[session] == nil {
		m.sources[session] = map[string]*source{}
	}
	cur := m.sources[session]
	wanted := map[string]ActiveTrack{}
	for _, t := range tracks {
		wanted[t.ID] = t
	}
	for id, s := range cur {
		if _, keep := wanted[id]; !keep {
			close(s.stop)
			delete(cur, id)
			removed = append(removed, id)
		}
	}
	for id, t := range wanted {
		if _, has := cur[id]; has {
			continue
		}
		s := &source{stop: make(chan struct{})}
		cur[id] = s
		added = append(added, id)
		go pump(t, s.stop, onFrame)
	}
	return added, removed
}

func (m *MediaRuntime) StopSession(session string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sources[session] {
		close(s.stop)
	}
	delete(m.sources, session)
}

// pump 是占位的本地媒体源：不做真实网络，仅周期性产出帧事件。
func pump(t ActiveTrack, stop <-chan struct{}, onFrame func(Frame)) {
	size := t.Params.BitrateBps.Min / 8 // 每秒约 8 帧时的近似字节数
	if size <= 0 {
		size = 100
	}
	var seq uint64
	for {
		select {
		case <-stop:
			return
		default:
		}
		select {
		case <-stop:
			return
		case <-tick():
			seq++
			if onFrame != nil {
				onFrame(Frame{TrackID: t.ID, Sender: t.Sender, Seq: seq, Bytes: size})
			}
		}
	}
}

func (t ActiveTrack) String() string {
	kind := "audio"
	if t.Kind == KindVideo {
		kind = "video"
	}
	return fmt.Sprintf("%s[%s %s->%s %d-%dbps]", t.ID, kind, t.Sender, t.Receiver, t.Params.BitrateBps.Min, t.Params.BitrateBps.Max)
}
