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

func newMultiService(t *testing.T) *Service {
	t.Helper()
	service := newTestService(t)
	service.RegisterPeer(Peer{ID: "carol", Capabilities: testCapabilities()})
	if _, _, err := service.JoinSession("call", "carol", "Carol"); err != nil {
		t.Fatal(err)
	}
	return service
}

func TestPartialAcceptanceThenLeaveFinalizes(t *testing.T) {
	service := newMultiService(t)
	offer, err := service.CreateOffer("call", "alice", testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	_, version, err := service.AnswerOffer("call", "bob", offer.Revision, testFullProposal())
	if err != nil || version.Status != VersionPartial {
		t.Fatalf("expected partial after one answer, got %v %v", version, err)
	}
	if session, _ := service.Session("call"); len(session.Tracks) != 0 {
		t.Fatalf("partial offer must not change session, got %+v", session.Tracks)
	}
	if _, err := service.LeaveSession("call", "carol"); err != nil {
		t.Fatal(err)
	}
	version, _ = service.Version("call", offer.Revision)
	if version.Status != VersionAccepted {
		t.Fatalf("departed target must not block acceptance, got %s", version.Status)
	}
	if _, _, err := service.AnswerOffer("call", "carol", offer.Revision, testFullProposal()); !errors.Is(err, ErrParticipant) {
		t.Fatalf("late consent from removed identity must fail, got %v", err)
	}
}

func TestRejoinMintsNewGenerationAndConsentIsNotStitched(t *testing.T) {
	service := newMultiService(t)
	offer, _ := service.CreateOffer("call", "alice", testFullProposal())
	if _, _, err := service.AnswerOffer("call", "carol", offer.Revision, testFullProposal()); err != nil {
		t.Fatal(err)
	}
	if _, v, err := service.AnswerOffer("call", "bob", offer.Revision, testFullProposal()); err != nil || v.Status != VersionAccepted {
		t.Fatalf("setup offer failed: %v %v", v, err)
	}
	if _, err := service.LeaveSession("call", "carol"); err != nil {
		t.Fatal(err)
	}
	_, carol2, err := service.JoinSession("call", "carol", "Carol")
	if err != nil {
		t.Fatal(err)
	}
	if carol2.ParticipationID != "carol#2" {
		t.Fatalf("rejoin must mint a new generation, got %s", carol2.ParticipationID)
	}
	// The old generation's consent must not complete the new offer round.
	second, err := service.CreateOffer("call", "alice", testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(second.Targets, "carol#2") || containsString(second.Targets, "carol") {
		t.Fatalf("targets must use current generations only: %+v", second.Targets)
	}
	if _, v, err := service.AnswerOffer("call", "bob", second.Revision, testFullProposal()); err != nil || v.Status != VersionPartial {
		t.Fatalf("old generation consent must not stitch into acceptance: %v %v", v, err)
	}
	if _, _, err := service.AnswerOffer("call", "carol", second.Revision, testFullProposal()); !errors.Is(err, ErrParticipant) {
		t.Fatalf("old identity must not answer, got %v", err)
	}
	if _, v, err := service.AnswerOffer("call", "carol#2", second.Revision, testFullProposal()); err != nil || v.Status != VersionAccepted {
		t.Fatalf("new generation consent should complete: %v %v", v, err)
	}
	_ = offer
}

func TestSessionForShowsOnlyAcceptedSources(t *testing.T) {
	service := newMultiService(t)
	offer, _ := service.CreateOffer("call", "alice", testFullProposal())
	if _, _, err := service.AnswerOffer("call", "bob", offer.Revision, testFullProposal()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.AnswerOffer("call", "carol", offer.Revision, testFullProposal()); err != nil {
		t.Fatal(err)
	}
	carolOffer, err := service.CreateOffer("call", "carol", testAudioOnlyProposal())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.AnswerOffer("call", "alice", carolOffer.Revision, testAudioOnlyProposal()); err != nil {
		t.Fatal(err)
	}
	// bob explicitly declines carol's tracks; the offer still completes.
	declined := testAudioOnlyProposal()
	for i := range declined {
		declined[i].Enabled = false
	}
	if _, v, err := service.AnswerOffer("call", "bob", carolOffer.Revision, declined); err != nil || v.Status != VersionAccepted {
		t.Fatalf("offer should complete once all targets answered: %v %v", v, err)
	}
	// bob never accepted carol's tracks: bob sees alice's tracks only.
	bobView, err := service.SessionFor("call", "bob")
	if err != nil {
		t.Fatal(err)
	}
	for _, track := range bobView.Tracks {
		if track.Owner == "carol" {
			t.Fatalf("bob must not see sources it never accepted: %+v", track)
		}
	}
	if len(bobView.Tracks) != 2 {
		t.Fatalf("bob should see alice's two tracks, got %+v", bobView.Tracks)
	}
	carolView, _ := service.SessionFor("call", "carol")
	if len(carolView.Tracks) != 3 { // own audio + alice's accepted two
		t.Fatalf("carol view mismatch: %+v", carolView.Tracks)
	}
	if _, err := service.SessionFor("call", "carol#9"); !errors.Is(err, ErrParticipant) {
		t.Fatalf("unknown generation must be rejected, got %v", err)
	}
}

func TestLeaveWithdrawsTracksAndFreesTrackIDs(t *testing.T) {
	service := newMultiService(t)
	offer, _ := service.CreateOffer("call", "carol", testAudioOnlyProposal())
	service.AnswerOffer("call", "alice", offer.Revision, testAudioOnlyProposal())
	if _, v, err := service.AnswerOffer("call", "bob", offer.Revision, testAudioOnlyProposal()); err != nil || v.Status != VersionAccepted {
		t.Fatalf("setup offer failed: %v %v", v, err)
	}
	if _, err := service.LeaveSession("call", "carol"); err != nil {
		t.Fatal(err)
	}
	session, _ := service.Session("call")
	for _, track := range session.Tracks {
		if track.Owner == "carol" {
			t.Fatalf("leaver tracks must be withdrawn: %+v", track)
		}
	}
	if _, carol2, _ := service.JoinSession("call", "carol", "Carol"); carol2.ParticipationID != "carol#2" {
		t.Fatal("expected carol#2")
	}
	reoffer, err := service.CreateOffer("call", "carol#2", testAudioOnlyProposal())
	if err != nil {
		t.Fatalf("track ids must be reusable after withdrawal: %v", err)
	}
	service.AnswerOffer("call", "alice", reoffer.Revision, testAudioOnlyProposal())
	if _, v, err := service.AnswerOffer("call", "bob", reoffer.Revision, testAudioOnlyProposal()); err != nil || v.Status != VersionAccepted {
		t.Fatalf("reoffer with reused ids failed: %v %v", v, err)
	}
}

func TestDuplicateAnswerIdempotentAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	build := func() *Service {
		service, err := NewService(NewStore(dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"alice", "bob", "carol"} {
			service.RegisterPeer(Peer{ID: id, Capabilities: testCapabilities()})
		}
		return service
	}
	service := build()
	if _, err := service.CreateSession("call", "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.JoinSession("call", "carol", "Carol"); err != nil {
		t.Fatal(err)
	}
	offer, _ := service.CreateOffer("call", "alice", testFullProposal())
	if _, _, err := service.AnswerOffer("call", "bob", offer.Revision, testFullProposal()); err != nil {
		t.Fatal(err)
	}

	reopened := build()
	_, dup, err := reopened.AnswerOffer("call", "bob", offer.Revision, testFullProposal())
	if err != nil {
		t.Fatal(err)
	}
	if len(dup.Responses) != 1 || dup.Status != VersionPartial {
		t.Fatalf("duplicate answer must be a no-op, got %+v", dup)
	}
	if _, v, err := reopened.AnswerOffer("call", "carol", offer.Revision, testFullProposal()); err != nil || v.Status != VersionAccepted {
		t.Fatalf("final answer should complete after reopen: %v %v", v, err)
	}
	if _, stale, err := reopened.AnswerOffer("call", "bob", offer.Revision, testFullProposal()); !errors.Is(err, ErrStale) || stale.Status != VersionAccepted {
		t.Fatalf("answer after acceptance must be stale, got %v %v", stale, err)
	}
}
