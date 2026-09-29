package session

import "sync"

// SessionMeta 只保存会话自身信息。
type SessionMeta struct {
	ID      string
	Peers   []string
	Version int64
}

// TrackState 只保存轨道运行时状态；停用轨道时删除对应条目，释放状态。
type TrackState struct {
	Track  ActiveTrack
	Active bool
}

// NegotiationRecord 只保存协商版本与双方意图，独立于会话与轨道存储。
type NegotiationRecord struct {
	Version   int64
	Intents   map[string]*Intent // 每个端点最新已接受的意图
	ActiveIDs []string
}

// Store 把会话元数据、轨道、协商版本分成三个独立存储。
type Store struct {
	mu sync.RWMutex

	meta   map[string]*SessionMeta
	tracks map[string]map[string]*TrackState // sessionID -> trackID
	nego   map[string]*NegotiationRecord
}

func NewStore() *Store {
	return &Store{
		meta:   map[string]*SessionMeta{},
		tracks: map[string]map[string]*TrackState{},
		nego:   map[string]*NegotiationRecord{},
	}
}

func (s *Store) Create(id string, peers []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta[id] = &SessionMeta{ID: id, Peers: append([]string(nil), peers...), Version: 0}
	s.tracks[id] = map[string]*TrackState{}
	s.nego[id] = &NegotiationRecord{Version: 0, Intents: map[string]*Intent{}}
}

func (s *Store) Snapshot(id string) (SessionMeta, map[string]TrackState, NegotiationRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.meta[id]
	if !ok {
		return SessionMeta{}, nil, NegotiationRecord{}, false
	}
	tracks := map[string]TrackState{}
	for k, v := range s.tracks[id] {
		tracks[k] = *v
	}
	n := *s.nego[id]
	intents := map[string]*Intent{}
	for k, v := range n.Intents {
		cp := *v
		intents[k] = &cp
	}
	n.Intents = intents
	n.ActiveIDs = append([]string(nil), n.ActiveIDs...)
	return *m, tracks, n, true
}

// Commit 用协商结果原子地落盘：版本号前进、轨道表按差集增删。
// 返回停用（释放）与新增的轨道 ID。
func (s *Store) Commit(id string, version int64, active []ActiveTrack, intents map[string]*Intent) (added, removed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.meta[id]
	if version <= meta.Version {
		return nil, nil // 旧版本提交，拒绝覆盖
	}
	old := s.tracks[id]
	next := map[string]*TrackState{}
	wanted := map[string]bool{}
	for i := range active {
		t := active[i]
		wanted[t.ID] = true
		next[t.ID] = &TrackState{Track: t, Active: true}
		if _, had := old[t.ID]; !had {
			added = append(added, t.ID)
		}
	}
	for tid := range old {
		if !wanted[tid] {
			removed = append(removed, tid) // 停用轨道：删除并释放状态
		}
	}
	s.tracks[id] = next
	meta.Version = version

	n := s.nego[id]
	n.Version = version
	n.ActiveIDs = n.ActiveIDs[:0]
	for k, v := range intents {
		cp := *v
		n.Intents[k] = &cp
		n.ActiveIDs = append(n.ActiveIDs, k)
	}
	return added, removed
}
