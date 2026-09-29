// 演示双方能力协商与会话变更：交叉消息、迟到答复、停用轨道、断开恢复，
// 媒体由本地模拟端提供，全部在进程内完成，不依赖通信平台。
package main

import (
	"fmt"
	"time"

	"avsession/internal/coord"
	"avsession/internal/session"
	"avsession/internal/transport"
)

const sess = "room-1"

func main() {
	store := session.NewStore()
	store.Create(sess, []string{"alice", "bob"})
	engine := session.NewEngine(store)
	media := session.NewMediaRuntime()
	link := transport.NewLink(64)

	// 双方能力：码率区间与可接受帧率/采样率。共同范围是协商成功的前提。
	engine.Register(sess, "alice", session.Capability{
		Name: "alice-device", Bitrate: session.Range{Min: 50_000, Max: 500_000},
		FrameRates: []int{15, 30}, SampleRates: []int{16000, 48000},
	})
	engine.Register(sess, "bob", session.Capability{
		Name: "bob-device", Bitrate: session.Range{Min: 100_000, Max: 300_000},
		FrameRates: []int{15, 24, 30}, SampleRates: []int{48000},
	})

	replyCh := make(chan string, 32)
	onEvent := func(e coord.Event) {
		switch e.Kind {
		case "committed":
			fmt.Printf("[server] v%d committed (+%v -%v) tracks=%v\n", e.Version, e.Added, e.Removed, e.Tracks)
		case "waiting":
			fmt.Printf("[server] %s 已答复，等待对端 (当前 v%d)\n", e.Peer, e.Version)
		case "stale":
			fmt.Printf("[server] 忽略 %s 的迟到答复（旧版本，当前 v%d）\n", e.Peer, e.Version)
		case "failed":
			fmt.Printf("[server] 协商失败，保留旧会话：%s\n", e.Reason)
		}
	}
	server := coord.NewServer(engine, media, link, onEvent)
	link.Join("server", server.HandleConfigure)

	alice := coord.NewPeer("alice", sess, link, func(r coord.ConfigureReply) {
		replyCh <- fmt.Sprintf("alice <- code=%d v%d %s", r.Code, r.Version, r.Reason)
	})
	bob := coord.NewPeer("bob", sess, link, func(r coord.ConfigureReply) {
		replyCh <- fmt.Sprintf("bob   <- code=%d v%d %s", r.Code, r.Version, r.Reason)
	})

	audio := func(sender string) map[string]session.TrackSpec {
		if sender == "alice" {
			return map[string]session.TrackSpec{
				"mic": {Kind: session.KindAudio, Direction: session.DirSendOnly,
					Params: session.MediaParams{BitrateBps: session.Range{Min: 100_000, Max: 120_000}, SampleRate: 48000}},
			}
		}
		return map[string]session.TrackSpec{
			"mic": {Kind: session.KindAudio, Direction: session.DirRecvOnly,
				Params: session.MediaParams{BitrateBps: session.Range{Min: 100_000, Max: 120_000}, SampleRate: 48000}},
		}
	}
	video := func(dir session.Direction, br session.Range, fps int) map[string]session.TrackSpec {
		return map[string]session.TrackSpec{
			"mic": {Kind: session.KindAudio, Direction: session.DirSendOnly, Params: session.MediaParams{BitrateBps: session.Range{Min: 100_000, Max: 120_000}, SampleRate: 48000}},
			"cam": {Kind: session.KindVideo, Direction: dir, Params: session.MediaParams{BitrateBps: br, Framerate: fps}},
		}
	}

	drain := func() {
		for {
			select {
			case m := <-replyCh:
				fmt.Println("  " + m)
			default:
				return
			}
		}
	}
	wait := func(d time.Duration) { time.Sleep(d); drain() }

	fmt.Println("== 1) 初始协商：alice 发音频，bob 收 ==")
	alice.Configure(audio("alice"))
	bob.Configure(audio("bob"))
	wait(50 * time.Millisecond)
	fmt.Printf("  视图：alice v%d, bob v%d\n\n", alice.Version(), bob.Version())

	fmt.Println("== 2) 双方同时改配置（交叉消息）：都想加视频，固定规则小 ID 裁决 ==")
	alice.Configure(video(session.DirSendOnly, session.Range{Min: 200_000, Max: 250_000}, 30))
	bob.Configure(video(session.DirRecvOnly, session.Range{Min: 200_000, Max: 250_000}, 30))
	wait(50 * time.Millisecond)
	fmt.Printf("  视图：alice v%d, bob v%d, tracks=%v\n\n", alice.Version(), bob.Version(), bob.Tracks())

	fmt.Println("== 3) 协商失败：alice 要 60fps（超出共同能力），bob 同意；旧会话保留 ==")
	alice.Configure(video(session.DirSendOnly, session.Range{Min: 200_000, Max: 250_000}, 60))
	bob.Configure(video(session.DirRecvOnly, session.Range{Min: 200_000, Max: 250_000}, 60))
	wait(50 * time.Millisecond)
	fmt.Printf("  失败后版本不变：alice v%d, bob v%d，mic+cam 仍在\n\n", alice.Version(), bob.Version())

	fmt.Println("== 4) bob 断线期间，alice 发起停用视频；服务器等待，版本不变 ==")
	link.Disconnect("bob")
	alice.Configure(audio("alice")) // 只到 alice 一方，进入 waiting
	wait(50 * time.Millisecond)
	fmt.Printf("  bob 离线，alice 侧版本保持 v%d，cam 尚未释放\n\n", alice.Version())

	fmt.Println("== 5) bob 恢复并答复 -> v3 提交、cam 释放；随后一条旧 v2 答复被忽略 ==")
	n := link.Reconnect("bob")
	wait(30 * time.Millisecond)
	bob.Configure(audio("bob")) // 与 alice 同基于 v2 的答复，到齐提交 v3
	wait(60 * time.Millisecond)
	fmt.Printf("  恢复投递缓存 %d 条；alice v%d, bob v%d, tracks=%v\n", n, alice.Version(), bob.Version(), bob.Tracks())

	// 人为注入一条迟到答复：base 停留在 v2，落后于当前 v3，必须被忽略。
	link.Disconnect("bob")
	bob.ConfigureAt(2, video(session.DirRecvOnly, session.Range{Min: 200_000, Max: 250_000}, 30))
	link.Reconnect("bob")
	wait(60 * time.Millisecond)
	fmt.Printf("  迟到答复后仍一致：alice v%d, bob v%d, tracks=%v\n", alice.Version(), bob.Version(), bob.Tracks())
	fmt.Println("\n说明：媒体帧由本地模拟源按生效轨道产生，停用即停止并释放 goroutine。")
	media.StopSession(sess)
}
