package main

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
)

const DefaultSessionID = "call-1"

func main() {
	dataDir := flag.String("data", filepath.Join(".", "runtime-data"), "directory for independent session, track and negotiation JSON files")
	flag.Parse()

	store := NewStore(*dataDir)
	service, err := NewService(store)
	if err != nil {
		panic(err)
	}
	alice := Peer{ID: "alice", Capabilities: demoCapabilities()}
	bob := Peer{ID: "bob", Capabilities: demoCapabilities()}
	carol := Peer{ID: "carol", Capabilities: carolCapabilities()}
	dave := Peer{ID: "dave", Capabilities: demoCapabilities()}
	for _, peer := range []Peer{alice, bob, carol, dave} {
		service.RegisterPeer(peer)
	}
	if _, err := service.CreateSession(DefaultSessionID, bob.ID, alice.ID); err != nil {
		panic(err)
	}

	bus := NewBus()
	endpoints := map[string]*Endpoint{}
	for _, peer := range []Peer{alice, bob, carol, dave} {
		endpoints[peer.ID] = NewEndpoint(DefaultSessionID, peer.ID, service, bus)
		endpoints[peer.ID].Connect()
	}
	media := &LocalMediaSource{}

	fmt.Println("== 两端初始协商：Alice 发起，Bob 确认 ==")
	if _, err := endpoints[alice.ID].OfferAll(fullProposal(500_000)); err != nil {
		panic(err)
	}
	printMediaFor(service, media, alice.ID, bob.ID)

	fmt.Println("\n== 同时加人：Carol 与 Dave 加入会话 ==")
	if _, err := service.JoinSession(DefaultSessionID, carol.ID); err != nil {
		panic(err)
	}
	if _, err := service.JoinSession(DefaultSessionID, dave.ID); err != nil {
		panic(err)
	}
	session, _ := service.Session(DefaultSessionID)
	fmt.Printf("当前成员：%s\n", joinPeers(session.Peers))

	fmt.Println("\n== 全员提案：Carol 视频编码无共同范围，只有部分人生效 ==")
	version, err := endpoints[alice.ID].OfferAll(fullProposal(600_000))
	if err != nil {
		panic(err)
	}
	version, _ = service.Version(DefaultSessionID, version.Revision)
	fmt.Printf("r%d 最终状态=%s，已确认=%s，未纳入=%s\n",
		version.Revision, version.Status, joinPeers(version.AcceptedBy), joinPeers(version.Declined))
	session, _ = service.Session(DefaultSessionID)
	fmt.Printf("会话生效范围（非全体一致）：%s\n", joinPeers(session.Scope))
	printMediaFor(service, media, alice.ID, bob.ID, carol.ID, dave.ID)

	fmt.Println("\n== 订阅调整：Carol 单方面订阅 audio-1 ==")
	if _, _, err := service.AdjustSubscription(DefaultSessionID, carol.ID, "audio-1", true); err != nil {
		panic(err)
	}
	printMediaFor(service, media, carol.ID)

	fmt.Println("\n== Dave 发布屏幕轨道 screen-1（Carol 仍因 video-1 冲突未纳入）==")
	if _, err := endpoints[dave.ID].OfferAll(screenShareProposal()); err != nil {
		panic(err)
	}
	printMediaFor(service, media, alice.ID, dave.ID)

	fmt.Println("\n== 断线追平：Carol 离线期间 Dave 移除 video-1，回来后追到当前状态 ==")
	endpoints[carol.ID].Disconnect()
	removeVersion, err := endpoints[dave.ID].OfferAll(audioScreenProposal())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Carol 离线，信箱排队 %d 条\n", bus.Queued(carol.ID))
	endpoints[carol.ID].Connect()
	endpoints[carol.ID].Sync()
	current, _ := service.Session(DefaultSessionID)
	fmt.Printf("Carol 的迟到答复若针对旧版本将被拒绝：")
	if _, _, err := service.AnswerOffer(DefaultSessionID, carol.ID, removeVersion.Revision-1, fullProposal(600_000)); errors.Is(err, ErrStale) {
		fmt.Println("stale，未覆盖 r", current.Revision)
	} else {
		panic(fmt.Sprintf("expected stale, got %v", err))
	}

	fmt.Println("\n== 成员离开：Dave 退出，其发布的 screen-1 被释放 ==")
	if _, err := service.LeaveSession(DefaultSessionID, dave.ID); err != nil {
		panic(err)
	}
	session, _ = service.Session(DefaultSessionID)
	fmt.Printf("剩余成员=%s，活动轨道=%s\n", joinPeers(session.Peers), describeTracks(session.Tracks))
	printMediaFor(service, media, alice.ID, bob.ID, carol.ID)

	fmt.Println("\n== 保存恢复：从磁盘重建服务，逐端核对轨道归属与协商历史 ==")
	restored, err := NewService(NewStore(*dataDir))
	if err != nil {
		panic(err)
	}
	for _, peer := range []Peer{alice, bob, carol, dave} {
		restored.RegisterPeer(peer)
	}
	for _, peerID := range []string{alice.ID, bob.ID, carol.ID} {
		view, tracks, err := restored.SyncView(DefaultSessionID, peerID)
		if err != nil {
			panic(err)
		}
		fmt.Printf("%s 视角：revision=r%d，轨道=%s\n", peerID, view.Revision, describeTracks(tracks))
	}
	history, err := restored.Versions(DefaultSessionID)
	if err != nil {
		panic(err)
	}
	fmt.Println("协商历史：")
	for _, entry := range history {
		fmt.Printf("  r%d by=%s status=%s accepted=%s declined=%s %s\n",
			entry.Revision, entry.Initiator, entry.Status,
			joinPeers(entry.AcceptedBy), joinPeers(entry.Declined), entry.Reason)
	}
}

func printMediaFor(service *Service, media *LocalMediaSource, peers ...string) {
	session, err := service.Session(DefaultSessionID)
	if err != nil {
		panic(err)
	}
	for _, peerID := range peers {
		frames := media.ProduceFor(peerID, session.Tracks)
		fmt.Printf("%s 本地产出 %d 帧：", peerID, len(frames))
		for i, frame := range frames {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Printf("%s#%d", frame.TrackID, frame.Sequence)
		}
		fmt.Println()
	}
}

func demoCapabilities() map[string]TrackCapability {
	return map[string]TrackCapability{
		"audio-1": {
			ID:          "audio-1",
			Kind:        "audio",
			Directions:  []string{string(DirectionSendRecv), string(DirectionInactive)},
			Codecs:      []string{"opus"},
			BitrateMin:  16_000,
			BitrateMax:  96_000,
			SampleRates: []int{48_000},
			Channels:    []int{1, 2},
		},
		"video-1": {
			ID:          "video-1",
			Kind:        "video",
			Directions:  []string{string(DirectionSendRecv), string(DirectionInactive)},
			Codecs:      []string{"VP8", "H264"},
			BitrateMin:  100_000,
			BitrateMax:  1_000_000,
			Resolutions: []Size{{Width: 640, Height: 480}, {Width: 1280, Height: 720}},
		},
		"screen-1": {
			ID:          "screen-1",
			Kind:        "video",
			Directions:  []string{string(DirectionSendRecv), string(DirectionInactive)},
			Codecs:      []string{"VP8"},
			BitrateMin:  200_000,
			BitrateMax:  2_000_000,
			Resolutions: []Size{{Width: 1280, Height: 720}, {Width: 1920, Height: 1080}},
		},
	}
}

func carolCapabilities() map[string]TrackCapability {
	capabilities := demoCapabilities()
	video := capabilities["video-1"]
	video.Codecs = []string{"AV1"}
	capabilities["video-1"] = video
	return capabilities
}

func fullProposal(videoBitrate int) []TrackProposal {
	return []TrackProposal{
		{
			ID:          "audio-1",
			Kind:        "audio",
			Enabled:     true,
			Direction:   string(DirectionSendRecv),
			Codecs:      []string{"opus"},
			BitrateMin:  32_000,
			BitrateMax:  64_000,
			SampleRates: []int{48_000},
			Channels:    []int{2},
		},
		{
			ID:          "video-1",
			Kind:        "video",
			Enabled:     true,
			Direction:   string(DirectionSendRecv),
			Codecs:      []string{"VP8", "H264"},
			BitrateMin:  videoBitrate,
			BitrateMax:  videoBitrate,
			Resolutions: []Size{{Width: 1280, Height: 720}},
		},
	}
}

func screenShareProposal() []TrackProposal {
	proposal := fullProposal(600_000)
	return append(proposal, TrackProposal{
		ID:          "screen-1",
		Kind:        "video",
		Enabled:     true,
		Direction:   string(DirectionSendRecv),
		Codecs:      []string{"VP8"},
		BitrateMin:  800_000,
		BitrateMax:  1_500_000,
		Resolutions: []Size{{Width: 1920, Height: 1080}},
	})
}

func audioScreenProposal() []TrackProposal {
	proposal := screenShareProposal()
	proposal[1].Enabled = false
	return proposal
}

func audioOnlyProposal() []TrackProposal {
	proposal := fullProposal(500_000)
	proposal[1].Enabled = false
	return proposal
}

func invalidCodecProposal() []TrackProposal {
	proposal := audioOnlyProposal()
	proposal[0].Codecs = []string{"G722"}
	return proposal
}
