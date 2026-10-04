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
	roomDemo := flag.Bool("room", false, "run the multi-party participation/track demo instead of the original two-party demo")
	flag.Parse()
	if *roomDemo {
		if err := runRoomDemo(filepath.Join(*dataDir, "room-demo")); err != nil {
			panic(err)
		}
		return
	}

	store := NewStore(*dataDir)
	service, err := NewService(store)
	if err != nil {
		panic(err)
	}
	alice := Peer{ID: "alice", Capabilities: demoCapabilities()}
	bob := Peer{ID: "bob", Capabilities: demoCapabilities()}
	service.RegisterPeer(alice)
	service.RegisterPeer(bob)
	if _, err := service.CreateSession(DefaultSessionID, bob.ID, alice.ID); err != nil {
		panic(err)
	}

	bus := NewBus()
	aliceEndpoint := NewEndpoint(DefaultSessionID, alice.ID, service, bus)
	bobEndpoint := NewEndpoint(DefaultSessionID, bob.ID, service, bus)
	aliceEndpoint.Connect()
	bobEndpoint.Connect()
	media := &LocalMediaSource{}

	fmt.Println("== 交叉消息：Alice 断线期间收到 Bob 提案，随后双方同时改配置 ==")
	aliceEndpoint.Disconnect()
	bobOffer, err := bobEndpoint.Offer(alice.ID, fullProposal(600_000))
	if err != nil {
		panic(err)
	}
	aliceOffer, err := aliceEndpoint.Offer(bob.ID, fullProposal(400_000))
	if err != nil {
		panic(err)
	}
	fmt.Printf("Bob r%d 与 Alice r%d 交叉；规则固定为字典序较小者优先\n", bobOffer.Revision, aliceOffer.Revision)
	aliceEndpoint.Connect()
	bobEndpoint.Connect()
	printMedia(service, media)

	fmt.Println("\n== 迟到答复重放：旧版本不能覆盖新版本 ==")
	_, staleVersion, err := service.AnswerOffer(DefaultSessionID, alice.ID, bobOffer.Revision, fullProposal(600_000))
	if !errors.Is(err, ErrStale) {
		panic(fmt.Sprintf("expected stale response, got %v", err))
	}
	fmt.Printf("r%d 状态=%s，当前会话未被覆盖\n", staleVersion.Revision, staleVersion.Status)

	fmt.Println("\n== 断开与恢复：Bob 断线时保留信箱，Alice 停用视频 ==")
	bobEndpoint.Disconnect()
	_, err = aliceEndpoint.Offer(bob.ID, audioOnlyProposal())
	if err != nil {
		panic(err)
	}
	fmt.Printf("Bob 离线，Alice 消息在本地信箱排队：%d 条\n", bus.Queued(bob.ID))
	bobEndpoint.Connect()
	printMedia(service, media)

	fmt.Println("\n== 协商失败：答复参数超出共同范围，旧会话保留 ==")
	failedVersion, err := service.CreateOffer(DefaultSessionID, alice.ID, audioOnlyProposal())
	if err != nil {
		panic(err)
	}
	_, failed, err := service.AnswerOffer(DefaultSessionID, bob.ID, failedVersion.Revision, invalidCodecProposal())
	if err == nil {
		panic("expected negotiation failure")
	}
	fmt.Printf("r%d 状态=%s：%s\n", failed.Revision, failed.Status, err)
	printMedia(service, media)

	session, _ := service.Session(DefaultSessionID)
	fmt.Printf("\n最终一致：session.revision=r%d，活动轨道=%s\n", session.Revision, describeTracks(session.Tracks))
}

func printMedia(service *Service, media *LocalMediaSource) {
	session, err := service.Session(DefaultSessionID)
	if err != nil {
		panic(err)
	}
	frames := media.Produce(session.Tracks)
	fmt.Printf("本地产出 %d 帧：", len(frames))
	for i, frame := range frames {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Printf("%s#%d", frame.TrackID, frame.Sequence)
	}
	fmt.Println()
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
	}
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
