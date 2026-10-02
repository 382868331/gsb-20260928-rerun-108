package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	service.RegisterPeer(Peer{ID: "alice", Capabilities: testCapabilities()})
	service.RegisterPeer(Peer{ID: "bob", Capabilities: testCapabilities()})
	if _, err := service.CreateSession("call", "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	return service
}

func TestSuccessfulNegotiationUsesCommonRange(t *testing.T) {
	service := newTestService(t)
	offer, err := service.CreateOffer("call", "alice", testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := service.AnswerOffer("call", "bob", offer.Revision, testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Tracks) != 2 {
		t.Fatalf("expected two active tracks, got %d", len(session.Tracks))
	}
	video := session.Tracks[1]
	if video.ID != "video-1" || video.Codec != "VP8" || video.BitrateBps != 500000 {
		t.Fatalf("unexpected negotiated video: %+v", video)
	}
}

func TestCrossedOfferFixedWinnerAndLateAnswerCannotOverwrite(t *testing.T) {
	service := newTestService(t)
	bobOffer, err := service.CreateOffer("call", "bob", testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	aliceOffer, err := service.CreateOffer("call", "alice", testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	if aliceOffer.Status != VersionProposing {
		t.Fatalf("alice offer should win, got %s", aliceOffer.Status)
	}
	bobVersion, _ := service.Version("call", bobOffer.Revision)
	if bobVersion.Status != VersionSuperseded {
		t.Fatalf("bob offer should be superseded, got %s", bobVersion.Status)
	}

	session, _, err := service.AnswerOffer("call", "bob", aliceOffer.Revision, testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	_, stale, err := service.AnswerOffer("call", "alice", bobOffer.Revision, testFullProposal())
	if !errors.Is(err, ErrStale) || stale.Status != VersionSuperseded {
		t.Fatalf("expected stale superseded response, got status=%s err=%v", stale.Status, err)
	}
	current, _ := service.Session("call")
	if current.Revision != session.Revision {
		t.Fatalf("late answer changed revision: got %d want %d", current.Revision, session.Revision)
	}
}

func TestFailedNegotiationKeepsOldSession(t *testing.T) {
	service := newTestService(t)
	first, _ := service.CreateOffer("call", "alice", testFullProposal())
	oldSession, _, err := service.AnswerOffer("call", "bob", first.Revision, testFullProposal())
	if err != nil {
		t.Fatal(err)
	}

	failedOffer, _ := service.CreateOffer("call", "alice", testAudioOnlyProposal())
	failedAnswer := testAudioOnlyProposal()
	failedAnswer[0].Codecs = []string{"G722"}
	_, version, err := service.AnswerOffer("call", "bob", failedOffer.Revision, failedAnswer)
	if err == nil || version.Status != VersionRejected {
		t.Fatalf("expected rejected negotiation, got err=%v status=%s", err, version.Status)
	}
	current, _ := service.Session("call")
	if current.Revision != oldSession.Revision || len(current.Tracks) != len(oldSession.Tracks) {
		t.Fatalf("old session changed: got %+v want %+v", current, oldSession)
	}
}

func TestDisablingTrackReleasesState(t *testing.T) {
	service := newTestService(t)
	offer, _ := service.CreateOffer("call", "alice", testFullProposal())
	if _, _, err := service.AnswerOffer("call", "bob", offer.Revision, testFullProposal()); err != nil {
		t.Fatal(err)
	}
	next, _ := service.CreateOffer("call", "alice", testAudioOnlyProposal())
	session, _, err := service.AnswerOffer("call", "bob", next.Revision, testAudioOnlyProposal())
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Tracks) != 1 || session.Tracks[0].ID != "audio-1" {
		t.Fatalf("video state was not released: %+v", session.Tracks)
	}
}

func TestDisconnectQueuesAndReconnectDelivers(t *testing.T) {
	service := newTestService(t)
	bus := NewBus()
	alice := NewEndpoint("call", "alice", service, bus)
	alice.Connect()
	bobEndpoint := NewEndpoint("call", "bob", service, bus)
	var received []Message
	originalHandler := bus.handlers["bob"]
	bus.Register("bob", func(message Message) {
		received = append(received, message)
		originalHandler(message)
	})
	bobEndpoint.Disconnect()
	if _, err := alice.Offer("bob", testFullProposal()); err != nil {
		t.Fatal(err)
	}
	if bus.Queued("bob") != 1 {
		t.Fatalf("expected one queued message, got %d", bus.Queued("bob"))
	}
	bobEndpoint.Connect()
	if len(received) != 1 || received[0].Revision != 1 {
		t.Fatalf("queued message was not delivered on recovery: %+v", received)
	}
}

func testCapabilities() map[string]TrackCapability {
	return map[string]TrackCapability{
		"audio-1": {
			ID:          "audio-1",
			Kind:        "audio",
			Directions:  []string{string(DirectionSendRecv), string(DirectionInactive)},
			Codecs:      []string{"opus"},
			BitrateMin:  16000,
			BitrateMax:  96000,
			SampleRates: []int{48000},
			Channels:    []int{1, 2},
		},
		"video-1": {
			ID:          "video-1",
			Kind:        "video",
			Directions:  []string{string(DirectionSendRecv), string(DirectionInactive)},
			Codecs:      []string{"VP8", "H264"},
			BitrateMin:  100000,
			BitrateMax:  1000000,
			Resolutions: []Size{{Width: 640, Height: 480}, {Width: 1280, Height: 720}},
		},
	}
}

func testFullProposal() []TrackProposal {
	return []TrackProposal{
		{ID: "audio-1", Kind: "audio", Enabled: true, Direction: string(DirectionSendRecv), Codecs: []string{"opus"}, BitrateMin: 32000, BitrateMax: 64000, SampleRates: []int{48000}, Channels: []int{2}},
		{ID: "video-1", Kind: "video", Enabled: true, Direction: string(DirectionSendRecv), Codecs: []string{"VP8"}, BitrateMin: 500000, BitrateMax: 500000, Resolutions: []Size{{Width: 1280, Height: 720}}},
	}
}

func testAudioOnlyProposal() []TrackProposal {
	proposal := testFullProposal()
	proposal[1].Enabled = false
	return proposal
}

func newMultiTestService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	service, err := NewService(NewStore(dir))
	if err != nil {
		t.Fatal(err)
	}
	service.RegisterPeer(Peer{ID: "alice", Capabilities: testCapabilities()})
	service.RegisterPeer(Peer{ID: "bob", Capabilities: testCapabilities()})
	carolCaps := testCapabilities()
	video := carolCaps["video-1"]
	video.Codecs = []string{"AV1"}
	carolCaps["video-1"] = video
	service.RegisterPeer(Peer{ID: "carol", Capabilities: carolCaps})
	if _, err := service.CreateSession("call", "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	return service, dir

}

func TestPartialAcceptanceScopeIsExplicit(t *testing.T) {
	service, _ := newMultiTestService(t)
	if _, err := service.JoinSession("call", "carol"); err != nil {
		t.Fatal(err)
	}
	offer, err := service.CreateOffer("call", "alice", testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	session, version, err := service.AnswerOffer("call", "bob", offer.Revision, testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	if version.Status != VersionPartial {
		t.Fatalf("expected partial while carol pending, got %s", version.Status)
	}
	if len(session.Scope) != 2 || session.Scope[0] != "alice" || session.Scope[1] != "bob" {
		t.Fatalf("scope must list only confirmed peers, got %v", session.Scope)
	}
	decline := testFullProposal()
	decline[1].Codecs = []string{"AV1"}
	session, version, err = service.AnswerOffer("call", "carol", offer.Revision, decline)
	if err == nil {
		t.Fatal("expected carol answer to fail on codec")
	}
	if version.Status != VersionPartial || len(version.Declined) != 1 || version.Declined[0] != "carol" {
		t.Fatalf("declined member must stay visible, got status=%s declined=%v", version.Status, version.Declined)
	}
	if len(session.Scope) != 2 {
		t.Fatalf("declined member must not enter scope, got %v", session.Scope)
	}
}

func TestJoinLeaveReleasesPublishedTracks(t *testing.T) {
	service, _ := newMultiTestService(t)
	if _, err := service.JoinSession("call", "carol"); err != nil {
		t.Fatal(err)
	}
	offer, _ := service.CreateOffer("call", "carol", testAudioOnlyProposal())
	if _, _, err := service.AnswerOffer("call", "alice", offer.Revision, testAudioOnlyProposal()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.AnswerOffer("call", "bob", offer.Revision, testAudioOnlyProposal()); err != nil {
		t.Fatal(err)
	}
	session, _ := service.Session("call")
	if len(session.Tracks) != 1 || session.Tracks[0].Publisher != "carol" {
		t.Fatalf("expected carol-owned audio track, got %+v", session.Tracks)
	}
	if _, err := service.LeaveSession("call", "carol"); err != nil {
		t.Fatal(err)
	}
	session, _ = service.Session("call")
	if len(session.Tracks) != 0 {
		t.Fatalf("leaver tracks must be released, got %+v", session.Tracks)
	}
	if len(session.Peers) != 2 {
		t.Fatalf("leaver must be removed from members, got %v", session.Peers)
	}
}

func TestReconnectCatchUpDoesNotOverwrite(t *testing.T) {
	service, _ := newMultiTestService(t)
	if _, err := service.JoinSession("call", "carol"); err != nil {
		t.Fatal(err)
	}
	bus := NewBus()
	alice := NewEndpoint("call", "alice", service, bus)
	alice.Connect()
	bobEndpoint := NewEndpoint("call", "bob", service, bus)
	bobEndpoint.Connect()
	carolEndpoint := NewEndpoint("call", "carol", service, bus)
	carolEndpoint.Connect()

	carolEndpoint.Disconnect()
	first, err := alice.OfferAll(testAudioOnlyProposal())
	if err != nil {
		t.Fatal(err)
	}
	if bus.Queued("carol") != 1 {
		t.Fatalf("expected queued offer for offline carol, got %d", bus.Queued("carol"))
	}
	carolEndpoint.Connect()
	session, view, err := service.SyncView("call", "carol")
	if err != nil {
		t.Fatal(err)
	}
	if session.Revision != first.Revision || len(view) != 1 {
		t.Fatalf("carol must catch up to r%d, got r%d view=%+v", first.Revision, session.Revision, view)
	}
	if _, _, err := service.AnswerOffer("call", "carol", first.Revision, testAudioOnlyProposal()); !errors.Is(err, ErrStale) {
		t.Fatalf("duplicate answer after catch-up must be stale, got %v", err)
	}
	current, _ := service.Session("call")
	if current.Revision != first.Revision {
		t.Fatalf("stale answer overwrote revision: got %d want %d", current.Revision, first.Revision)
	}
}

func TestSubscriptionAdjustSurvivesRestore(t *testing.T) {
	service, dir := newMultiTestService(t)
	if _, err := service.JoinSession("call", "carol"); err != nil {
		t.Fatal(err)
	}
	offer, _ := service.CreateOffer("call", "alice", testAudioOnlyProposal())
	service.AnswerOffer("call", "bob", offer.Revision, testAudioOnlyProposal())
	service.AnswerOffer("call", "carol", offer.Revision, testAudioOnlyProposal())
	if _, _, err := service.AdjustSubscription("call", "carol", "audio-1", false); err != nil {
		t.Fatal(err)
	}

	restored, err := NewService(NewStore(dir))
	if err != nil {
		t.Fatal(err)
	}
	_, view, err := restored.SyncView("call", "carol")
	if err != nil {
		t.Fatal(err)
	}
	if len(view) != 0 {
		t.Fatalf("unsubscribe must persist across restore, got %+v", view)
	}
	session, _ := restored.Session("call")
	if session.Tracks[0].Publisher != "alice" {
		t.Fatalf("ownership must survive restore, got %+v", session.Tracks[0])
	}
	history, err := restored.Versions("call")
	if err != nil {
		t.Fatal(err)
	}
	var subEntry *NegotiationVersion
	for i := range history {
		if history[i].Initiator == "carol" && history[i].Status == VersionAccepted {
			subEntry = &history[i]
		}
	}
	if subEntry == nil {
		t.Fatal("subscription change must appear in restored history")
	}
}
