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

func multiPartyService(t *testing.T) (*Service, string) {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alice", "bob", "carol"} {
		service.RegisterPeer(Peer{ID: id, Capabilities: testCapabilities()})
	}
	sessionID := "meet"
	if _, err := service.CreateSession(sessionID, "alice", "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BootstrapMultiParty(sessionID, []string{"alice", "bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddParticipant(sessionID, "carol", "Carol"); err != nil {
		t.Fatal(err)
	}
	return service, sessionID
}

func TestPartialAcceptanceIsPerParticipant(t *testing.T) {
	service, sessionID := multiPartyService(t)
	offer, err := service.CreateOffer(sessionID, "alice", testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	_, version, err := service.AnswerOffer(sessionID, "bob", offer.Revision, testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	if version.Status != VersionPartial {
		t.Fatalf("expected partial after one of two answers, got %s", version.Status)
	}
	audioOnly := testAudioOnlyProposal()
	_, version, err = service.AnswerOffer(sessionID, "carol", offer.Revision, audioOnly)
	if err != nil {
		t.Fatal(err)
	}
	if version.Status != VersionAccepted {
		t.Fatalf("expected accepted after both current targets answer, got %s", version.Status)
	}
	session, _ := service.Session(sessionID)
	video := findState(session.Tracks, "video-1")
	if video == nil || len(video.Subscribers) != 1 || video.Subscribers[0] != "bob" {
		t.Fatalf("video must be visible only to bob: %+v", video)
	}
	audio := findState(session.Tracks, "audio-1")
	if audio == nil || len(audio.Subscribers) != 2 {
		t.Fatalf("audio must be visible to bob and carol: %+v", audio)
	}
}

func TestSameNameReentryIsNewGenerationAndIsolatesState(t *testing.T) {
	service, sessionID := multiPartyService(t)
	offer, _ := service.CreateOffer(sessionID, "alice", testFullProposal())
	if _, _, err := service.AnswerOffer(sessionID, "bob", offer.Revision, testFullProposal()); err != nil {
		t.Fatal(err)
	}
	audioOnly := testAudioOnlyProposal()
	if _, _, err := service.AnswerOffer(sessionID, "carol", offer.Revision, audioOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LeaveParticipant(sessionID, "carol"); err != nil {
		t.Fatal(err)
	}
	session, _ := service.Session(sessionID)
	audio := findState(session.Tracks, "audio-1")
	if audio == nil || len(audio.Subscribers) != 1 || audio.Subscribers[0] != "bob" {
		t.Fatalf("carol subscription must be gone after leave: %+v", audio)
	}
	carol2, err := service.AddParticipant(sessionID, "carol", "Carol")
	if err != nil {
		t.Fatal(err)
	}
	if carol2.ParticipationID != "carol#2" || carol2.Generation != 2 {
		t.Fatalf("unexpected re-entry identity: %+v", carol2)
	}
	// New generation cannot complete the old generation's targeted version.
	_, _, err = service.AnswerOffer(sessionID, carol2.ParticipationID, offer.Revision, testFullProposal())
	if err != ErrParticipant {
		t.Fatalf("new generation must not answer old version, got %v", err)
	}
	old, _ := service.Version(sessionID, offer.Revision)
	if len(old.Responses) != 2 {
		t.Fatalf("old carol decision must remain auditable, got responses %v", old.Responses)
	}
}

func TestLateConsentAfterLeaveIsRecordedButNotApplied(t *testing.T) {
	service, sessionID := multiPartyService(t)
	offer, _ := service.CreateOffer(sessionID, "alice", testFullProposal())
	if _, _, err := service.AnswerOffer(sessionID, "carol", offer.Revision, testAudioOnlyProposal()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LeaveParticipant(sessionID, "bob"); err != nil {
		t.Fatal(err)
	}
	_, version, err := service.AnswerOffer(sessionID, "bob", offer.Revision, testFullProposal())
	if err != ErrParticipant {
		t.Fatalf("departed bob late answer must fail, got %v", err)
	}
	if len(version.Duplicates) != 1 || version.Duplicates[0].Responder != "bob" {
		t.Fatalf("late consent must be archived: %+v", version.Duplicates)
	}
	session, _ := service.Session(sessionID)
	if video := findState(session.Tracks, "video-1"); video != nil {
		t.Fatalf("late consent must not resurrect video: %+v", video)
	}
	if version.Status == VersionAccepted {
		t.Fatalf("departed consent must not complete acceptance, got %s", version.Status)
	}
}

func TestDuplicateAnswerAfterRestartIsStoredButNotApplied(t *testing.T) {
	service, sessionID := multiPartyService(t)
	offer, _ := service.CreateOffer(sessionID, "alice", testFullProposal())
	if _, _, err := service.AnswerOffer(sessionID, "bob", offer.Revision, testFullProposal()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.AnswerOffer(sessionID, "carol", offer.Revision, testAudioOnlyProposal()); err != nil {
		t.Fatal(err)
	}
	root := service.store.root
	reopened, err := NewService(NewStore(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alice", "bob", "carol"} {
		reopened.RegisterPeer(Peer{ID: id, Capabilities: testCapabilities()})
	}
	before, _ := reopened.Session(sessionID)
	_, version, err := reopened.AnswerOffer(sessionID, "bob", offer.Revision, testFullProposal())
	if err != ErrStale {
		t.Fatalf("duplicate after restart must be stale, got %v", err)
	}
	if len(version.Duplicates) != 1 {
		t.Fatalf("expected one archived duplicate, got %d", len(version.Duplicates))
	}
	after, _ := reopened.Session(sessionID)
	if len(after.Tracks) != len(before.Tracks) {
		t.Fatalf("duplicate answer changed active tracks")
	}
}

func findState(tracks []TrackState, id string) *TrackState {
	for i := range tracks {
		if tracks[i].ID == id {
			return &tracks[i]
		}
	}
	return nil
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
