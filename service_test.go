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
