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
	for _, id := range []string{"alice", "bob", "carol"} {
		service.RegisterPeer(Peer{ID: id, Capabilities: demoCapabilities()})
	}
	if _, err := service.CreateSession(DefaultSessionID, "bob", "alice"); err != nil {
		panic(err)
	}
	if _, carol, err := service.JoinSession(DefaultSessionID, "carol", "Carol"); err != nil {
		panic(err)
	} else {
		fmt.Printf("Carol 加入会话，参与身份=%s（世代 1）\n", carol.ParticipationID)
	}

	bus := NewBus()
	alice := NewEndpoint(DefaultSessionID, "alice", service, bus)
	bob := NewEndpoint(DefaultSessionID, "bob", service, bus)
	carol := NewEndpoint(DefaultSessionID, "carol", service, bus)
	alice.Connect()
	bob.Connect()
	media := &LocalMediaSource{}

	fmt.Println("\n== 部分成员接受：Carol 离线，Alice 提案只有 Bob 答复 ==")
	carol.Disconnect()
	aliceOffer, err := alice.OfferAll(fullProposal(600_000))
	if err != nil {
		panic(err)
	}
	version, _ := service.Version(DefaultSessionID, aliceOffer.Revision)
	fmt.Printf("r%d 状态=%s（Bob 已接受，Carol 未答复，暂不入会话）\n", version.Revision, version.Status)

	fmt.Println("\n== 成员离开与迟到同意同时发生：Carol(世代1) 离开，旧答复不再计数 ==")
	if _, err := service.LeaveSession(DefaultSessionID, "carol"); err != nil {
		panic(err)
	}
	version, _ = service.Version(DefaultSessionID, aliceOffer.Revision)
	fmt.Printf("Carol 离开后 r%d 状态=%s，目标只剩在场成员\n", version.Revision, version.Status)
	if _, _, err := service.AnswerOffer(DefaultSessionID, "carol", aliceOffer.Revision, fullProposal(600_000)); !errors.Is(err, ErrParticipant) {
		panic(fmt.Sprintf("expected ErrParticipant for departed identity, got %v", err))
	}
	fmt.Println("旧身份 carol 的迟到同意被拒绝（ErrParticipant），不影响新状态")
	carol.Connect() // 投递排队消息，旧身份端点自行忽略

	fmt.Println("\n== 同名重入：Carol 重新加入成为新参与世代 ==")
	_, carol2, err := service.JoinSession(DefaultSessionID, "carol", "Carol")
	if err != nil {
		panic(err)
	}
	fmt.Printf("新参与身份=%s，旧身份 carol 的消息与答复均不影响它\n", carol2.ParticipationID)
	carol2Endpoint := NewEndpoint(DefaultSessionID, carol2.ParticipationID, service, bus)
	carol2Endpoint.Connect()

	fmt.Println("\n== 原轨道编号重新使用：新 Carol 用相同轨道 ID 发布 ==")
	if _, err := carol2Endpoint.OfferAll(fullProposal(500_000)); err != nil {
		panic(err)
	}
	printViews(service, media, "alice", "bob", carol2.ParticipationID)

	fmt.Println("\n== 保存重开后的重复答复：重载服务，重复答复幂等 ==")
	reopened, err := NewService(store)
	if err != nil {
		panic(err)
	}
	for _, id := range []string{"alice", "bob", "carol"} {
		reopened.RegisterPeer(Peer{ID: id, Capabilities: demoCapabilities()})
	}
	if _, stale, err := reopened.AnswerOffer(DefaultSessionID, "bob", aliceOffer.Revision, fullProposal(600_000)); errors.Is(err, ErrStale) {
		fmt.Printf("重开后 Bob 对已完结 r%d 的迟到答复返回 stale，旧决定保留可查（答复数=%d）\n", stale.Revision, len(stale.Responses))
	} else {
		panic(fmt.Sprintf("expected stale after reopen, got %v", err))
	}
	partial, err := reopened.CreateOffer(DefaultSessionID, "alice", audioOnlyProposal())
	if err != nil {
		panic(err)
	}
	if _, v, err := reopened.AnswerOffer(DefaultSessionID, "bob", partial.Revision, audioOnlyProposal()); err != nil || v.Status != VersionPartial {
		panic(fmt.Sprintf("expected partial after first answer, got %v %s", err, v.Status))
	}
	if _, dup, err := reopened.AnswerOffer(DefaultSessionID, "bob", partial.Revision, audioOnlyProposal()); err != nil {
		panic(err)
	} else if len(dup.Responses) != 1 || dup.Status != VersionPartial {
		panic(fmt.Sprintf("duplicate answer not idempotent: %+v", dup))
	} else {
		fmt.Printf("重开后 Bob 对进行中 r%d 的重复答复幂等：答复数=%d，状态=%s\n", dup.Revision, len(dup.Responses), dup.Status)
	}
	if _, v, err := reopened.AnswerOffer(DefaultSessionID, "carol#2", partial.Revision, audioOnlyProposal()); err != nil || v.Status != VersionAccepted {
		panic(fmt.Sprintf("expected accepted after carol#2, got %v %v", v, err))
	} else {
		fmt.Printf("carol#2 答复后 r%d 状态=%s（仅本世代同意被计入）\n", v.Revision, v.Status)
	}

	session, _ := reopened.Session(DefaultSessionID)
	fmt.Printf("\n最终一致：session.revision=r%d，活动轨道=%s\n", session.Revision, describeTracks(session.Tracks))
	for _, p := range session.Participants {
		fmt.Printf("参与记录：%s（%s）present=%v online=%v\n", p.ParticipationID, p.DisplayName, p.Present, p.Online)
	}
}

func printViews(service *Service, media *LocalMediaSource, participationIDs ...string) {
	for _, pid := range participationIDs {
		view, err := service.SessionFor(DefaultSessionID, pid)
		if err != nil {
			panic(err)
		}
		frames := media.Produce(view.Tracks)
		fmt.Printf("%s 视角：轨道=[%s]，本地产出 %d 帧\n", pid, describeTracks(view.Tracks), len(frames))
	}
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
