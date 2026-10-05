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

	runTwoPartyDemo(*dataDir)
	fmt.Println()
	runMultiPartyDemo(*dataDir)
}

func runTwoPartyDemo(dataDir string) {
	store := NewStore(filepath.Join(dataDir, "two-party"))
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

	fmt.Println("== 两端协商恢复：交叉消息与固定规则冲突 ==")
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
	printMedia(service, media, DefaultSessionID)

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
	printMedia(service, media, DefaultSessionID)

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
	printMedia(service, media, DefaultSessionID)

	session, _ := service.Session(DefaultSessionID)
	fmt.Printf("\n两端最终一致：session.revision=r%d，活动轨道=%s\n", session.Revision, describeTracks(session.Tracks))
}

func runMultiPartyDemo(dataDir string) {
	fmt.Println("========== 多人会话：参与身份、轨道来源与按参与者订阅 ==========")
	root := filepath.Join(dataDir, "multi-party")
	store := NewStore(root)
	service, err := NewService(store)
	must(err)
	caps := demoCapabilities()
	for _, personID := range []string{"alice", "bob", "carol"} {
		service.RegisterPeer(Peer{ID: personID, Capabilities: caps})
	}
	sessionID := "meet-1"
	must2(service.CreateSession(sessionID, "alice", "bob"))
	must2(service.BootstrapMultiParty(sessionID, []string{"alice", "bob"}))
	carol1, err := service.AddParticipant(sessionID, "carol", "Carol")
	must(err)

	bus := NewBus()
	aliceEP := NewEndpoint(sessionID, "alice", service, bus)
	bobEP := NewEndpoint(sessionID, "bob", service, bus)
	carolEP := NewEndpoint(sessionID, carol1.ParticipationID, service, bus)
	// Everyone registers offline so the demo can control exactly which
	// generation accepts and when queued messages get delivered.
	carolEP.SetAcceptPolicy(func(proposal TrackProposal) bool { return proposal.Kind == "audio" })

	fmt.Println("== 一部分成员接受：Alice 发布音频+视频，Carol 只接受音频 ==")
	aliceEP.Connect()
	offerVersion, err := aliceEP.OfferTargets([]string{"bob", carol1.ParticipationID}, fullProposal(500_000))
	must(err)
	fmt.Printf("Bob、Carol 均离线，信箱排队：bob=%d carol=%d\n", bus.Queued("bob"), bus.Queued(carol1.ParticipationID))
	bobEP.Connect()
	afterBob, _ := service.Version(sessionID, offerVersion.Revision)
	fmt.Printf("仅 Bob 接受：r%d 状态=%s，Carol 尚未同意\n", offerVersion.Revision, afterBob.Status)
	printVisibility("Alice", aliceEP)
	printVisibility("Bob", bobEP)
	carolEP.Connect()
	printVisibility("Alice", aliceEP)
	printVisibility("Bob", bobEP)
	printVisibility("Carol#1", carolEP)

	fmt.Println("\n== Carol#1 离开后同名重入：新世代 Carol#2 不继承旧订阅与连接身份 ==")
	carolEP.Disconnect()
	_, err = service.LeaveParticipant(sessionID, carol1.ParticipationID)
	must(err)
	carolEP.Close()
	printVisibility("Bob（Carol 离开后）", bobEP)
	carol2, err := service.AddParticipant(sessionID, "carol", "Carol")
	must(err)
	fmt.Printf("重入身份=%s person=%s generation=%d\n", carol2.ParticipationID, carol2.PersonID, carol2.Generation)
	carol2EP := NewEndpoint(sessionID, carol2.ParticipationID, service, bus)
	carol2EP.Connect()
	printVisibility("Carol#2（无历史订阅）", carol2EP)

	fmt.Println("\n== 原轨道编号重新使用：Carol#2 复用本地编号 video-1，来源标记为新世代 ==")
	aliceEP.Disconnect()
	next, err := carol2EP.OfferTargets([]string{"alice", "bob"}, fullProposal(400_000))
	must(err)
	fmt.Printf("Alice 离线、Bob 在线：排队 alice=%d，Bob 自动接受\n", bus.Queued("alice"))
	aliceEP.Connect()
	printVisibility("Alice", aliceEP)
	printVisibility("Bob", bobEP)
	printVisibility("Carol#2", carol2EP)

	fmt.Println("\n== 移除与迟到同意同时发生：撤下视频期间 Bob 离场，其迟到同意不恢复视频 ==")
	bobEP.Disconnect()
	withdraw, err := carol2EP.OfferTargets([]string{"alice", "bob"}, audioOnlyProposal())
	must(err)
	fmt.Printf("撤下提案同时发给在线 Alice 与离线 Bob：bob 队列=%d\n", bus.Queued("bob"))
	_, err = service.LeaveParticipant(sessionID, "bob")
	must(err)
	fmt.Printf("Bob 离场：其信箱与旧连接身份 %s 一并注销，排队消息丢弃\n", "bob")
	_, staleLate, lateErr := service.AnswerOffer(sessionID, "bob", withdraw.Revision, fullProposal(400_000))
	lateStatus := "(版本不存在)"
	if staleLate != nil {
		lateStatus = string(staleLate.Status)
	}
	fmt.Printf("模拟 Bob 迟到同意：%v（r%d=%s，视频未恢复）\n", lateErr, withdraw.Revision, lateStatus)
	bobEP.Close()
	printVisibility("Alice（撤下视频后）", aliceEP)
	printVisibility("Carol#2", carol2EP)

	fmt.Println("\n== 旧身份消息不得影响新身份：Carol#2 不能替 Carol#1 答复旧版本 ==")
	_, _, err = service.AnswerOffer(sessionID, carol2.ParticipationID, offerVersion.Revision, fullProposal(500_000))
	fmt.Printf("新世代答复旧目标版本：%v\n", err)
	printVisibility("Alice（轨道无变化）", aliceEP)

	fmt.Println("\n== 保存重开后的重复答复：重新加载服务后重复答复只留档、不生效 ==")
	reopened, err := NewService(NewStore(root))
	must(err)
	for _, personID := range []string{"alice", "bob", "carol"} {
		reopened.RegisterPeer(Peer{ID: personID, Capabilities: caps})
	}
	reopenedSession, err := reopened.Session(sessionID)
	must(err)
	fmt.Printf("重开后活动轨道：%s\n", describeTracks(reopenedSession.Tracks))
	_, _, err = reopened.AnswerOffer(sessionID, "alice", next.Revision, fullProposal(400_000))
	fmt.Printf("重复答复返回：%v\n", err)
	reopenedVersion, err := reopened.Version(sessionID, next.Revision)
	must(err)
	fmt.Printf("r%d 留档 %d 条重复答复，活动轨道保持不变\n", reopenedVersion.Revision, len(reopenedVersion.Duplicates))

	fmt.Println("\n== 历史决定可查：不同参与世代的同意不会拼成全体接受 ==")
	first, _ := service.Version(sessionID, offerVersion.Revision)
	fmt.Printf("r%d 状态=%s 答复世代=%v 目标快照=%v\n", first.Revision, first.Status, keysOf(first.Responses), first.Targets)
	withdrawn, _ := service.Version(sessionID, withdraw.Revision)
	fmt.Printf("r%d 状态=%s（Bob 已离场，不补齐为全体接受）\n", withdrawn.Revision, withdrawn.Status)
}

func keysOf(responses map[string]PeerResponse) []string {
	out := make([]string, 0, len(responses))
	for id := range responses {
		out = append(out, id)
	}
	return out
}

func printVisibility(name string, endpoint *Endpoint) {
	tracks := endpoint.VisibleTracks()
	fmt.Printf("%s 可见 %d 个来源：%s\n", name, len(tracks), describeTracks(tracks))
}

func printMedia(service *Service, media *LocalMediaSource, sessionID string) {
	session, err := service.Session(sessionID)
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

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func must2(_ any, err error) { must(err) }
