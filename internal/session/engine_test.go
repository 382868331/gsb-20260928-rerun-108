package session

import (
	"errors"
	"testing"
)

func audio(dir Direction, br Range) TrackSpec {
	return TrackSpec{Kind: KindAudio, Direction: dir, Params: MediaParams{BitrateBps: br, SampleRate: 48000}}
}

func newTestEngine(t *testing.T) (*Engine, *Store) {
	t.Helper()
	store := NewStore()
	store.Create("s", []string{"a", "b"})
	e := NewEngine(store)
	e.Register("s", "a", Capability{Bitrate: Range{50_000, 500_000}, SampleRates: []int{48000}})
	e.Register("s", "b", Capability{Bitrate: Range{100_000, 300_000}, SampleRates: []int{48000}})
	return e, store
}

func configure(t *testing.T, e *Engine, peer string, seq int64, base int64, tracks map[string]TrackSpec) Result {
	t.Helper()
	r, err := e.Configure(Intent{SessionID: "s", PeerID: peer, Seq: seq, Base: base, Tracks: tracks})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return r
}

func TestInitialNegotiationAndIntersection(t *testing.T) {
	e, _ := newTestEngine(t)
	r1 := configure(t, e, "a", 1, 0, map[string]TrackSpec{"mic": audio(DirSendOnly, Range{100_000, 200_000})})
	if r1.Code != ResultWaiting {
		t.Fatalf("want waiting, got %v", r1.Code)
	}
	r2 := configure(t, e, "b", 1, 0, map[string]TrackSpec{"mic": audio(DirRecvOnly, Range{100_000, 200_000})})
	if r2.Code != ResultCommitted || r2.Version != 1 {
		t.Fatalf("want committed v1, got %+v", r2)
	}
	if len(r2.Tracks) != 1 || r2.Tracks[0].Sender != "a" {
		t.Fatalf("unexpected tracks: %+v", r2.Tracks)
	}
}

func TestCrossedMessagesConflictResolvedBySmallerPeer(t *testing.T) {
	e, _ := newTestEngine(t)
	// 双方都想发送（冲突），固定规则取小 ID 方 a 作为发送者。
	configure(t, e, "a", 1, 0, map[string]TrackSpec{"mic": audio(DirSendOnly, Range{100_000, 200_000})})
	r := configure(t, e, "b", 1, 0, map[string]TrackSpec{"mic": audio(DirSendOnly, Range{150_000, 250_000})})
	if r.Code != ResultCommitted {
		t.Fatalf("want committed, got %v", r.Code)
	}
	if got := r.Tracks[0]; got.Sender != "a" || got.Params.BitrateBps.Max != 200_000 {
		t.Fatalf("conflict not resolved by peer a: %+v", got)
	}
}

func TestParamsOutsideCommonRangeFailsAndKeepsOldSession(t *testing.T) {
	e, store := newTestEngine(t)
	// 建立 v1。
	configure(t, e, "a", 1, 0, map[string]TrackSpec{"mic": audio(DirSendOnly, Range{100_000, 200_000})})
	configure(t, e, "b", 1, 0, map[string]TrackSpec{"mic": audio(DirRecvOnly, Range{100_000, 200_000})})

	// 下一轮 a 要求码率超出 b 的能力上限，a 先到（等待），b 到齐后整轮失败。
	configure(t, e, "a", 2, 1, map[string]TrackSpec{"mic": audio(DirSendOnly, Range{400_000, 450_000})})
	_, err := e.Configure(Intent{SessionID: "s", PeerID: "b", Seq: 2, Base: 1,
		Tracks: map[string]TrackSpec{"mic": audio(DirSendOnly, Range{400_000, 450_000})}})
	if err == nil || !errors.Is(err, ErrNegotiation) {
		t.Fatalf("want negotiation error, got %v", err)
	}

	meta, tracks, rec, _ := store.Snapshot("s")
	if meta.Version != 1 || rec.Version != 1 {
		t.Fatalf("old session must be kept, got meta=%d nego=%d", meta.Version, rec.Version)
	}
	if len(tracks) != 1 {
		t.Fatalf("old tracks must remain, got %d", len(tracks))
	}
}

func TestStaleAnswerNeverOverwrites(t *testing.T) {
	e, _ := newTestEngine(t)
	configure(t, e, "a", 1, 0, map[string]TrackSpec{"mic": audio(DirSendOnly, Range{100_000, 200_000})})
	configure(t, e, "b", 1, 0, map[string]TrackSpec{"mic": audio(DirRecvOnly, Range{100_000, 200_000})})

	// v1 已提交，再收到一条 base=0 的迟到答复：必须标记 stale 且版本不回退。
	r := configure(t, e, "b", 99, 0, map[string]TrackSpec{"mic": audio(DirRecvOnly, Range{100_000, 200_000})})
	if r.Code != ResultStale || r.Version != 1 {
		t.Fatalf("want stale at v1, got %+v", r)
	}
}

func TestDisablingTrackReleasesState(t *testing.T) {
	e, store := newTestEngine(t)
	configure(t, e, "a", 1, 0, map[string]TrackSpec{"mic": audio(DirSendOnly, Range{100_000, 200_000})})
	configure(t, e, "b", 1, 0, map[string]TrackSpec{"mic": audio(DirRecvOnly, Range{100_000, 200_000})})

	// a 停用 mic；按固定规则小 ID 方优先，轨道应从独立轨道存储中删除。
	configure(t, e, "a", 2, 1, map[string]TrackSpec{"mic": audio(DirInactive, Range{100_000, 200_000})})
	r := configure(t, e, "b", 2, 1, map[string]TrackSpec{"mic": audio(DirRecvOnly, Range{100_000, 200_000})})
	if r.Code != ResultCommitted || len(r.Tracks) != 0 || len(r.Removed) != 1 || r.Removed[0] != "mic" {
		t.Fatalf("track not released: %+v", r)
	}
	_, tracks, _, _ := store.Snapshot("s")
	if len(tracks) != 0 {
		t.Fatalf("track store should be empty, got %d", len(tracks))
	}
}

func TestRangeIntersect(t *testing.T) {
	if got, ok := (Range{1, 10}).Intersect(Range{5, 20}); !ok || got != (Range{5, 10}) {
		t.Fatalf("intersection wrong: %+v %v", got, ok)
	}
	if _, ok := (Range{1, 4}).Intersect(Range{5, 9}); ok {
		t.Fatal("disjoint ranges must not intersect")
	}
}
