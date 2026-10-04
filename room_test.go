package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func newRoomHarness(t *testing.T) (*RoomService, *RoomBus, string) {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	service, err := NewRoomService(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob", "carol", "dave"} {
		service.RegisterCapabilities(name, roomCaps())
	}
	return service, NewRoomBus(), "room"
}

func join(t *testing.T, service *RoomService, bus *RoomBus, room, name string) *RoomEndpoint {
	t.Helper()
	participant, err := service.Join(room, name)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := NewRoomEndpoint(room, participant.ID, service, bus, false)
	endpoint.Connect()
	return endpoint
}

func roomCaps() map[string]TrackCapability {
	return map[string]TrackCapability{
		"mic": {
			ID:          "mic",
			Kind:        "audio",
			Directions:  []string{"sendrecv", "sendonly", "recvonly", "inactive"},
			Codecs:      []string{"opus", "G722"},
			BitrateMin:  16000,
			BitrateMax:  96000,
			SampleRates: []int{48000},
			Channels:    []int{1, 2},
		},
		"cam": {
			ID:          "cam",
			Kind:        "video",
			Directions:  []string{"sendrecv", "sendonly", "recvonly", "inactive"},
			Codecs:      []string{"VP8", "H264"},
			BitrateMin:  100000,
			BitrateMax:  1000000,
			Resolutions: []Size{{Width: 640, Height: 480}, {Width: 1280, Height: 720}},
		},
	}
}

func micParams(bitrate int) MediaParams {
	return MediaParams{Direction: "sendrecv", Codec: "opus", BitrateBps: bitrate, SampleRate: 48000, Channels: 2}
}

func camParams(bitrate int) MediaParams {
	return MediaParams{Direction: "sendrecv", Codec: "VP8", BitrateBps: bitrate, Width: 1280, Height: 720}
}

func visibleIDs(view *Overview) map[string]MediaParams {
	out := map[string]MediaParams{}
	for _, track := range view.Visible {
		out[track.ID+"#g"+itoa(track.OwnerGeneration)] = track.Params
	}
	return out
}

func TestMultiPartyPartialAcceptanceAndPerParticipantView(t *testing.T) {
	service, bus, room := newRoomHarness(t)
	alice := join(t, service, bus, room, "alice")
	bob := join(t, service, bus, "room", "bob")
	carol := join(t, service, bus, room, "carol")

	if _, err := alice.Publish("mic", "audio", micParams(64000)); err != nil {
		t.Fatal(err)
	}
	adjust, err := alice.Offer("mic", micParams(64000), []string{bob.PID(), carol.PID()})
	if err != nil {
		t.Fatal(err)
	}
	if len(adjust.Items) != 2 {
		t.Fatalf("expected one item per participant, got %d", len(adjust.Items))
	}

	// Bob 接受；Carol 明确拒绝。协商保持部分接受状态，按端分别可见。
	if _, dup, err := bob.Respond(adjust.ID, 0, true, micParams(64000), ""); err != nil || dup {
		t.Fatalf("bob accept failed: %v dup=%v", err, dup)
	}
	if _, _, err := carol.Respond(adjust.ID, 1, false, MediaParams{}, "do not want audio"); err != nil {
		t.Fatal(err)
	}

	bobView, err := bob.Resync()
	if err != nil {
		t.Fatal(err)
	}
	carolView, err := carol.Resync()
	if err != nil {
		t.Fatal(err)
	}
	if len(bobView.Visible) != 1 || bobView.Visible[0].Params.BitrateBps != 64000 {
		t.Fatalf("bob should receive mic at 64000: %+v", bobView.Visible)
	}
	if len(carolView.Visible) != 0 {
		t.Fatalf("carol rejected the offer and must see no media: %+v", carolView.Visible)
	}
	stored, err := service.Snapshot(room, alice.PID())
	if err != nil {
		t.Fatal(err)
	}
	target := stored.Adjustments[adjust.ID].Items[1]
	if target.Status != ItemRejected || target.Rejections[carol.PID()] != "do not want audio" {
		t.Fatalf("rejection scope not preserved: %+v", target)
	}
}

func TestLeaveAfterPartialAcceptStopsOnlyDependentWork(t *testing.T) {
	service, bus, room := newRoomHarness(t)
	alice := join(t, service, bus, room, "alice")
	bob := join(t, service, bus, "room", "bob")
	carol := join(t, service, bus, room, "carol")

	if _, err := alice.Publish("mic", "audio", micParams(64000)); err != nil {
		t.Fatal(err)
	}
	first, err := alice.Offer("mic", micParams(64000), []string{bob.PID(), carol.PID()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := bob.Respond(first.ID, 0, true, micParams(64000), ""); err != nil {
		t.Fatal(err)
	}
	// Carol 尚未答复时离开：她那一项取消；Bob 已生效的订阅在其离开前仍保留。
	if err := service.Leave(room, carol.PID()); err != nil {
		t.Fatal(err)
	}
	stored, err := service.Snapshot(room, alice.PID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Adjustments[first.ID].Items[1].Status != ItemCancelled {
		t.Fatalf("carol pending item should be cancelled: %+v", stored.Adjustments[first.ID].Items[1])
	}

	// Bob 离开后，他的已生效订阅撤回，但 Alice 可继续与其他在场者协商。
	if err := service.Leave(room, bob.PID()); err != nil {
		t.Fatal(err)
	}
	if err := service.Leave(room, alice.PID()); err != nil {
		t.Fatal(err)
	}
	dave := join(t, service, bus, room, "dave")
	alice2 := join(t, service, bus, room, "alice")
	if _, err := alice2.Publish("mic", "audio", micParams(48000)); err != nil {
		t.Fatal(err)
	}
	second, err := alice2.Offer("mic", micParams(48000), []string{dave.PID()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dave.Respond(second.ID, 0, true, micParams(48000), ""); err != nil {
		t.Fatal(err)
	}
	view, _ := dave.Resync()
	if len(view.Visible) != 1 || view.Visible[0].OwnerPID != alice2.PID() || view.Visible[0].Params.BitrateBps != 48000 {
		t.Fatalf("independent negotiation after leave failed: %+v", view.Visible)
	}
}

func TestSameNameRejoinHasDistinctIdentityAndLateAnswerRejected(t *testing.T) {
	service, bus, room := newRoomHarness(t)
	alice := join(t, service, bus, room, "alice")
	bob1 := join(t, service, bus, room, "bob")

	// Bob 先断线，Alice 的提案进入 bob#1 自己的信箱。
	bob1.Disconnect()
	if _, err := alice.Publish("mic", "audio", micParams(64000)); err != nil {
		t.Fatal(err)
	}
	pending, err := alice.Offer("mic", micParams(64000), []string{bob1.PID()})
	if err != nil {
		t.Fatal(err)
	}
	if bus.Queued(bob1.PID()) != 1 {
		t.Fatalf("expected queued offer for old participation: %d", bus.Queued(bob1.PID()))
	}
	if err := service.Leave(room, bob1.PID()); err != nil {
		t.Fatal(err)
	}
	// 同名 bob 重新加入，得到新的参与身份 bob#2。
	bob2 := join(t, service, bus, room, "bob")
	if bob2.PID() == bob1.PID() {
		t.Fatalf("rejoin must produce a different pid: %s", bob2.PID())
	}
	// 旧连接的迟到答复不能修改协商，也不能以新连接身份生效。
	if _, _, err := bob1.Respond(pending.ID, 0, true, micParams(64000), ""); !errors.Is(err, ErrStaleParticipant) {
		t.Fatalf("old participation late answer must be rejected, got %v", err)
	}
	overview, err := bob2.Resync()
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Visible) != 0 {
		t.Fatalf("new participation must not inherit withdrawn/undecided subscription: %+v", overview.Visible)
	}
	// 新参与身份必须显式订阅；显式订阅后才产生媒体。
	req, err := bob2.Subscribe("mic", micParams(64000))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.Respond(req.ID, 0, true, micParams(64000), ""); err != nil {
		t.Fatal(err)
	}
	view, _ := bob2.Resync()
	if len(view.Visible) != 1 || view.Visible[0].OwnerPID != alice.PID() || view.Visible[0].Params.BitrateBps != 64000 {
		t.Fatalf("explicit re-subscription failed: %+v", view.Visible)
	}
	// 旧信箱与新连接完全隔离。
	if len(bob2.Connect()) != 0 {
		t.Fatal("old queued message leaked to the new participation")
	}
}

func TestRemoveTrackAndStaleAnswerArriveTogether(t *testing.T) {
	service, bus, room := newRoomHarness(t)
	alice := join(t, service, bus, room, "alice")
	bob := join(t, service, bus, "room", "bob")
	carol := join(t, service, bus, room, "carol")

	if _, err := alice.Publish("cam", "video", camParams(500000)); err != nil {
		t.Fatal(err)
	}
	adjust, err := alice.Offer("cam", camParams(500000), []string{bob.PID(), carol.PID()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := bob.Respond(adjust.ID, 0, true, camParams(500000), ""); err != nil {
		t.Fatal(err)
	}
	// 轨道被移除的同时，Carol 的迟到接受到达：不能重新激活已移除来源。
	if _, err := alice.Remove("cam"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := carol.Respond(adjust.ID, 1, true, camParams(500000), ""); err != nil {
		t.Fatalf("late answer against removed track should be recorded cancelled, got %v", err)
	}
	stored, _ := service.Snapshot(room, alice.PID())
	if stored.CurrentTrack("cam") != nil {
		t.Fatal("removed track source must stay unpublished")
	}
	for _, sub := range stored.Subscriptions {
		if sub.TrackID == "cam" && sub.Status == SubActive {
			t.Fatalf("late answer reactivated a removed track: %+v", sub)
		}
	}
	bobView, _ := bob.Resync()
	carolView, _ := carol.Resync()
	if len(bobView.Visible) != 0 || len(carolView.Visible) != 0 {
		t.Fatalf("nobody receives removed track: bob=%+v carol=%+v", bobView.Visible, carolView.Visible)
	}
}

func TestDuplicateAnswerAfterRestoreIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	store := NewStore(dataDir + "/data")
	service, err := NewRoomService(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		service.RegisterCapabilities(name, roomCaps())
	}
	bus := NewRoomBus()
	alice := join(t, service, bus, "room", "alice")
	bob := join(t, service, bus, "room", "bob")
	if _, err := alice.Publish("mic", "audio", micParams(64000)); err != nil {
		t.Fatal(err)
	}
	adjust, err := alice.Offer("mic", micParams(64000), []string{bob.PID()})
	if err != nil {
		t.Fatal(err)
	}
	result, duplicate, err := bob.Respond(adjust.ID, 0, true, micParams(64000), "")
	if err != nil || duplicate {
		t.Fatalf("first answer failed: %v dup=%v", err, duplicate)
	}
	revisionAfterFirst := result.BasedOn
	_ = revisionAfterFirst

	// 模拟进程恢复：从同一 JSON 文件重新加载服务后重放同一答复。
	restored, err := NewRoomService(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		restored.RegisterCapabilities(name, roomCaps())
	}
	replayed, duplicate, err := restored.Respond("room", bob.PID(), adjust.ID, 0, true, micParams(32000), "")
	if err != nil || !duplicate {
		t.Fatalf("replayed answer must be idempotent: err=%v duplicate=%v", err, duplicate)
	}
	if replayed.Items[0].Status != ItemAccepted || replayed.Items[0].Active.BitrateBps != 64000 {
		t.Fatalf("replay changed the established decision: %+v", replayed.Items[0])
	}
	snapshot, err := restored.Snapshot("room", alice.PID())
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, sub := range snapshot.Subscriptions {
		if sub.Status == SubActive && sub.TrackID == "mic" {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("duplicate answer created duplicate active subscriptions: %d", active)
	}
}

func TestCrossedProposalsOnSameTrackKeepSeparateBases(t *testing.T) {
	service, bus, room := newRoomHarness(t)
	alice := join(t, service, bus, room, "alice")
	bob := join(t, service, bus, room, "bob")
	carol := join(t, service, bus, room, "carol")

	if _, err := alice.Publish("mic", "audio", micParams(64000)); err != nil {
		t.Fatal(err)
	}
	// Alice 向 Bob 发起 offer（adj 0）；在它未决时，Carol 主动发起订阅请求
	// （adj 1）。两个提案针对同一轨道但基于不同参与身份，独立记录。
	toBob, err := alice.Offer("mic", micParams(64000), []string{bob.PID()})
	if err != nil {
		t.Fatal(err)
	}
	toCarol, err := carol.Subscribe("mic", micParams(32000))
	if err != nil {
		t.Fatal(err)
	}
	if toBob.BasedOn == toCarol.BasedOn && toBob.InitiatorPID == toCarol.InitiatorPID {
		t.Fatal("crossed proposals collapsed into one")
	}
	if _, _, err := bob.Respond(toBob.ID, 0, true, micParams(64000), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.Respond(toCarol.ID, 0, true, micParams(32000), ""); err != nil {
		t.Fatal(err)
	}
	stored, _ := service.Snapshot(room, alice.PID())
	accepts := map[string]bool{}
	for _, sub := range stored.Subscriptions {
		if sub.Status == SubActive {
			accepts[sub.SubscriberPID] = true
		}
	}
	if !accepts[bob.PID()] || !accepts[carol.PID()] {
		t.Fatalf("agreements from distinct participations were not kept separate: %+v", accepts)
	}
	bobView, _ := bob.Resync()
	carolView, _ := carol.Resync()
	if bobView.Visible[0].Params.BitrateBps != 64000 || carolView.Visible[0].Params.BitrateBps != 32000 {
		t.Fatalf("per-endpoint effective params were mixed: bob=%+v carol=%+v", bobView.Visible, carolView.Visible)
	}
}

func TestTrackIDReuseAfterSameNameRejoinDoesNotConfuseSource(t *testing.T) {
	service, bus, room := newRoomHarness(t)
	alice := join(t, service, bus, room, "alice")
	bob1 := join(t, service, bus, room, "bob")
	if _, err := bob1.Publish("mic", "audio", micParams(64000)); err != nil {
		t.Fatal(err)
	}
	req1, err := alice.Subscribe("mic", micParams(64000))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := bob1.Respond(req1.ID, 0, true, micParams(64000), ""); err != nil {
		t.Fatal(err)
	}
	view1, _ := alice.Resync()
	if len(view1.Visible) != 1 || view1.Visible[0].OwnerPID != bob1.PID() {
		t.Fatalf("expected media from %s, got %+v", bob1.PID(), view1.Visible)
	}
	// Bob 离开后移除其来源；同名 bob 重新加入并复用同一轨道 ID。
	if err := service.Leave(room, bob1.PID()); err != nil {
		t.Fatal(err)
	}
	bob2 := join(t, service, bus, room, "bob")
	if _, err := bob2.Publish("mic", "audio", micParams(48000)); err != nil {
		t.Fatal(err)
	}
	// Alice 概况里旧来源必须消失；即使 ID 相同也不是同一条轨道。
	viewBefore, _ := alice.Resync()
	if len(viewBefore.Visible) != 0 {
		t.Fatalf("old-source subscription leaked across generations: %+v", viewBefore.Visible)
	}
	overview, _ := bob2.Resync()
	if overview.Published[0].ID != "mic" || overview.Published[0].OwnerGeneration != 2 {
		t.Fatalf("resync overview must identify the new source generation: %+v", overview.Published)
	}
	req2, err := alice.Subscribe("mic", micParams(48000))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := bob2.Respond(req2.ID, 0, true, micParams(48000), ""); err != nil {
		t.Fatal(err)
	}
	view2, _ := alice.Resync()
	if len(view2.Visible) != 1 || view2.Visible[0].OwnerGeneration != 2 || view2.Visible[0].Params.BitrateBps != 48000 {
		t.Fatalf("reused ID must bind to the new source: %+v", view2.Visible)
	}
}

func TestWithdrawnSubscriptionIsNotRestoredAndMediaMatchesViews(t *testing.T) {
	service, bus, room := newRoomHarness(t)
	alice := join(t, service, bus, room, "alice")
	bob := join(t, service, bus, room, "bob")
	alice.Publish("mic", "audio", micParams(64000))
	adjust, _ := alice.Offer("mic", micParams(64000), []string{bob.PID()})
	bob.Respond(adjust.ID, 0, true, micParams(64000), "")
	media := &RoomMediaSource{}
	view, _ := bob.Resync()
	frames := media.Produce(view)
	if len(frames) != 1 || frames[0].TrackID != "mic" || frames[0].Params.BitrateBps != 64000 {
		t.Fatalf("expected one frame at the negotiated params: %+v", frames)
	}
	if err := bob.Unsubscribe("mic", 1); err != nil {
		t.Fatal(err)
	}
	viewAfter, _ := bob.Resync()
	if len(viewAfter.Visible) != 0 || len(media.Produce(viewAfter)) != 0 {
		t.Fatal("withdrawn subscription must immediately stop media")
	}
	// 发布者再发一次相同提案：旧拒绝/撤回记录不能被新提案当作接受。
	again, err := alice.Offer("mic", micParams(64000), []string{bob.PID()})
	if err != nil {
		t.Fatal(err)
	}
	if again.Items[0].Status != ItemPending {
		t.Fatalf("new proposal must remain pending for an explicit answer: %+v", again.Items[0])
	}
	viewPending, _ := bob.Resync()
	if len(viewPending.Visible) != 0 {
		t.Fatal("media started before the new participation explicitly accepted")
	}
}
