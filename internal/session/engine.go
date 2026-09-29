package session

import (
	"fmt"
	"sort"
)

// ResultCode 表示一次配置提交的处理结果。
type ResultCode int

const (
	ResultCommitted ResultCode = iota // 协商成功并产生新版本
	ResultWaiting                     // 等待对端同版本答复
	ResultStale                       // 迟到答复，已被忽略
)

type Result struct {
	Code    ResultCode
	Version int64
	Tracks  []ActiveTrack
	Added   []string
	Removed []string
}

type pending struct {
	base    int64
	intents map[string]*Intent
}

// Engine 是与通信平台无关的协商内核。
type Engine struct {
	store *Store
	caps  map[string]map[string]Capability // sessionID -> peerID
	pend  map[string]*pending
}

func NewEngine(store *Store) *Engine {
	return &Engine{store: store, caps: map[string]map[string]Capability{}, pend: map[string]*pending{}}
}

func (e *Engine) Register(id, peer string, c Capability) {
	if e.caps[id] == nil {
		e.caps[id] = map[string]Capability{}
	}
	e.caps[id][peer] = c
}

// Configure 处理一端的配置。只有双方基于同一版本的答复到齐才提交，
// 从而天然支持交叉消息；迟到答复（base 落后）直接忽略，不覆盖新版本。
func (e *Engine) Configure(in Intent) (Result, error) {
	meta, _, rec, ok := e.store.Snapshot(in.SessionID)
	if !ok {
		return Result{}, fmt.Errorf("%w: unknown session", ErrNegotiation)
	}
	peerOK := false
	for _, p := range meta.Peers {
		if p == in.PeerID {
			peerOK = true
		}
	}
	if !peerOK {
		return Result{}, fmt.Errorf("%w: unknown peer %q", ErrNegotiation, in.PeerID)
	}

	// 迟到答复：它看到的版本早于当前版本，忽略，绝不回退新版本。
	if in.Base < rec.Version {
		return Result{Code: ResultStale, Version: rec.Version}, nil
	}
	if in.Base > rec.Version {
		return Result{}, fmt.Errorf("%w: base version %d ahead of %d", ErrNegotiation, in.Base, rec.Version)
	}

	p := e.pend[in.SessionID]
	if p == nil || p.base != rec.Version {
		p = &pending{base: rec.Version, intents: map[string]*Intent{}}
		e.pend[in.SessionID] = p
	}
	if prev := p.intents[in.PeerID]; prev != nil && in.Seq <= prev.Seq {
		return Result{Code: ResultStale, Version: rec.Version}, nil // 重复/乱序的旧消息
	}
	cp := in
	p.intents[in.PeerID] = &cp

	if len(p.intents) != len(meta.Peers) {
		return Result{Code: ResultWaiting, Version: rec.Version}, nil
	}

	merged, err := e.merge(in.SessionID, p.intents)
	if err != nil {
		delete(e.pend, in.SessionID)
		return Result{}, err // 协商失败：pending 丢弃，已提交的旧会话保持不变
	}
	added, removed := e.store.Commit(in.SessionID, rec.Version+1, merged, p.intents)
	delete(e.pend, in.SessionID)
	return Result{
		Code: ResultCommitted, Version: rec.Version + 1,
		Tracks: merged, Added: added, Removed: removed,
	}, nil
}

func (e *Engine) merge(session string, intents map[string]*Intent) ([]ActiveTrack, error) {
	peers := make([]string, 0, len(intents))
	for id := range intents {
		peers = append(peers, id)
	}
	sort.Strings(peers)
	a, b := peers[0], peers[1] // 固定规则：端点 ID 较小者为冲突裁决优先方
	sa := specOrZero(intents[a].Tracks)
	sb := specOrZero(intents[b].Tracks)

	ids := map[string]struct{}{}
	for id := range sa {
		ids[id] = struct{}{}
	}
	for id := range sb {
		ids[id] = struct{}{}
	}
	idList := make([]string, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	sort.Strings(idList)

	var out []ActiveTrack
	for _, id := range idList {
		t, ok, err := mergeTrack(id, a, sa[id], b, sb[id], e.caps[session])
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, t)
		}
	}
	return out, nil
}

func specOrZero(m map[string]TrackSpec) map[string]TrackSpec {
	if m == nil {
		return map[string]TrackSpec{}
	}
	return m
}

// mergeTrack 按固定方向规则合并单条轨道。
// 冲突时以端点 ID 较小者 a 的意图为准；参数必须落在双方能力的共同范围内，
// 否则整轮协商失败（由上层保留旧会话）。
func mergeTrack(id, a string, sa TrackSpec, b string, sb TrackSpec, caps map[string]Capability) (ActiveTrack, bool, error) {
	if sa.Direction == DirInactive && sb.Direction == DirInactive {
		return ActiveTrack{}, false, nil // 双方停用：释放
	}

	sender, receiver, chosen := resolveDirections(a, b, sa, sb)
	if chosen.Direction == DirInactive {
		return ActiveTrack{}, false, nil // 优先方要求停用，轨道释放
	}

	ca, cb := caps[a], caps[b]
	if !ca.supports(chosen.Kind, chosen.Params) || !cb.supports(chosen.Kind, chosen.Params) {
		return ActiveTrack{}, false, fmt.Errorf("%w: track %q params outside common capability", ErrNegotiation, id)
	}
	if _, ok := ca.Bitrate.Intersect(cb.Bitrate); !ok {
		return ActiveTrack{}, false, fmt.Errorf("%w: track %q bitrate ranges do not intersect", ErrNegotiation, id)
	}
	return ActiveTrack{ID: id, Kind: chosen.Kind, Sender: sender, Receiver: receiver, Params: chosen.Params}, true, nil
}

// resolveDirections 裁决单条轨道的发送/接收方。
// 固定规则：方向不兼容（一方停用、双方都想发、双方都想收）时，端点 ID 较小的 a 优先，
// 对端被补成相反角色；参数采用 a 的，再由能力交集校验。
func resolveDirections(a, b string, sa, sb TrackSpec) (sender, receiver string, chosen TrackSpec) {
	if sa.Direction == DirInactive {
		return "", "", sa
	}
	as, ar := sa.Direction.sends(), sa.Direction.receives()
	bs, br := sb.Direction.sends(), sb.Direction.receives()
	// 方向互补：A 发 B 收，或 A 收 B 发。
	if as && br && !bs {
		return a, b, sa
	}
	if ar && bs && !as {
		return b, a, sa
	}
	if as && br {
		return a, b, sa
	}
	if ar && bs {
		return b, a, sa
	}
	// 冲突：小 ID 方优先。
	if sa.Direction.sends() {
		return a, b, sa
	}
	return b, a, sa
}
