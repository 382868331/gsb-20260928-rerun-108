package main

import (
	"errors"
	"fmt"
)

const DemoRoomID = "room-1"

// runRoomDemo 用本地信箱与模拟媒体源演练：部分接受后离开、同名重入、移除
// 轨道与旧答复同时到达、恢复保存状态后重复答复。
func runRoomDemo(dataDir string) error {
	store := NewStore(dataDir)
	service, err := NewRoomService(store)
	if err != nil {
		return err
	}
	for _, name := range []string{"alice", "bob", "carol"} {
		service.RegisterCapabilities(name, roomDemoCaps())
	}
	bus := NewRoomBus()
	joinEndpoint := func(name string) *RoomEndpoint {
		participant, err := service.Join(DemoRoomID, name)
		if err != nil {
			panic(err)
		}
		endpoint := NewRoomEndpoint(DemoRoomID, participant.ID, service, bus, true)
		endpoint.Connect()
		return endpoint
	}

	fmt.Println("== 多人轨道：Alice 发布 mic，向 Bob/Carol 发起按参与者订阅 ==")
	alice := joinEndpoint("alice")
	bob := joinEndpoint("bob")
	carol := joinEndpoint("carol")
	if _, err := alice.Publish("mic", "audio", roomMicParams(64000)); err != nil {
		return err
	}
	partial, err := alice.Offer("mic", roomMicParams(64000), []string{bob.PID(), carol.PID()})
	if err != nil {
		return err
	}
	if _, _, err := bob.Respond(partial.ID, 0, true, roomMicParams(64000), ""); err != nil {
		return err
	}
	if _, _, err := carol.Respond(partial.ID, 1, false, MediaParams{}, "暂时不收音频"); err != nil {
		return err
	}
	printRoomView("Bob", bob, service)
	printRoomView("Carol", carol, service)

	fmt.Println("\n== 部分接受后 Carol 离开：她的待决/接收内容停止，Bob 的决定保留 ==")
	if err := service.Leave(DemoRoomID, carol.PID()); err != nil {
		return err
	}
	if err := service.Leave(DemoRoomID, bob.PID()); err != nil {
		return err
	}
	if err := service.Leave(DemoRoomID, alice.PID()); err != nil {
		return err
	}

	fmt.Println("\n== 同名 Bob 重入：bob#2 取得概况后必须显式选择订阅 ==")
	alice2 := joinEndpoint("alice")
	bob2 := joinEndpoint("bob")
	if _, err := alice2.Publish("mic", "audio", roomMicParams(48000)); err != nil {
		return err
	}
	overview, err := bob2.Resync()
	if err != nil {
		return err
	}
	fmt.Printf("%s 概况：在场 %d 人，可订阅轨道 %d 条，实际可见 %d 条\n",
		bob2.PID(), len(overview.Present), len(overview.Published), len(overview.Visible))
	req, err := bob2.Subscribe("mic", roomMicParams(48000))
	if err != nil {
		return err
	}
	if _, _, err := alice2.Respond(req.ID, 0, true, roomMicParams(48000), ""); err != nil {
		return err
	}
	printRoomView("Bob#2", bob2, service)

	fmt.Println("\n== 旧连接迟到答复与轨道移除同时到达：不能激活已移除来源 ==")
	alice2.Publish("cam", "video", roomCamParams(500000))
	camOffer, err := alice2.Offer("cam", roomCamParams(500000), []string{bob2.PID()})
	if err != nil {
		return err
	}
	if _, err := alice2.Remove("cam"); err != nil {
		return err
	}
	if _, _, err := bob2.Respond(camOffer.ID, 0, true, roomCamParams(500000), ""); err != nil {
		return err
	}
	printRoomView("Bob#2-remove", bob2, service)

	fmt.Println("\n== 恢复保存状态后重复答复：幂等，不改变已生效参数 ==")
	restored, err := NewRoomService(store)
	if err != nil {
		return err
	}
	for _, name := range []string{"alice", "bob", "carol"} {
		restored.RegisterCapabilities(name, roomDemoCaps())
	}
	replayedEndpoint := &RoomEndpoint{roomID: DemoRoomID, pid: alice2.PID(), service: restored}
	replayed, duplicate, err := replayedEndpoint.Respond(req.ID, 0, true, roomMicParams(96000), "")
	if err != nil {
		return err
	}
	if !duplicate {
		return fmt.Errorf("expected idempotent duplicate response")
	}
	fmt.Printf("重复答复被忽略，生效参数仍为 %dbps\n", replayed.Items[0].Active.BitrateBps)

	// 旧连接重放：即使显示名称相同，旧参与身份的请求一律拒绝。
	if _, _, err := restored.Respond(DemoRoomID, bob.PID(), partial.ID, 0, true, roomMicParams(64000), ""); !errors.Is(err, ErrStaleParticipant) {
		return fmt.Errorf("expected stale participation error, got %v", err)
	}
	fmt.Printf("旧连接 %s 的迟到答复被拒绝：%v\n", bob.PID(), ErrStaleParticipant)
	return nil
}

func printRoomView(label string, endpoint *RoomEndpoint, service *RoomService) {
	view, err := endpoint.Resync()
	if err != nil {
		panic(err)
	}
	frames := (&RoomMediaSource{}).Produce(view)
	fmt.Printf("%s(%s) 可见 %d 条，媒体：%s\n", label, endpoint.PID(), len(view.Visible), describeRoomFrames(frames))
}

func roomDemoCaps() map[string]TrackCapability {
	return map[string]TrackCapability{
		"mic": {
			ID:          "mic",
			Kind:        "audio",
			Directions:  []string{"sendrecv", "sendonly", "recvonly", "inactive"},
			Codecs:      []string{"opus"},
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

func roomMicParams(bitrate int) MediaParams {
	return MediaParams{Direction: "sendrecv", Codec: "opus", BitrateBps: bitrate, SampleRate: 48000, Channels: 2}
}

func roomCamParams(bitrate int) MediaParams {
	return MediaParams{Direction: "sendrecv", Codec: "VP8", BitrateBps: bitrate, Width: 1280, Height: 720}
}
