# 音视频会话服务（第一版）

纯 Go、零第三方依赖。第一版聚焦**双方能力协商与会话变更**，媒体数据由本地模拟端产生，
后续可在 `session.MediaRuntime` 与 `transport` 边界接入真实传输。

## 模块

- `internal/session/types.go`：方向/类型、码率闭区间、能力、意图（`Intent`）。
- `internal/session/store.go`：**会话元数据、轨道状态、协商版本三个独立存储**，
  提交按版本号做乐观并发，旧版本提交被拒绝。
- `internal/session/engine.go`：协商内核。
- `internal/session/media.go`：本地模拟媒体源；轨道新增即起、停用即停并释放 goroutine。
- `internal/transport`：进程内可控管道，支持离线缓存、恢复重发、注入丢包，不依赖通信平台。
- `internal/coord`：最小服务器与端视图；端侧用 CAS 维护最新版本，旧快照不覆盖新版本。
- `cmd/demo`：五个场景的可运行演示。

## 关键规则

- 双方基于**同一 base 版本**的答复到齐才提交，天然支持交叉消息。
- 冲突固定规则：端 ID 较小者优先（发送方向与参数以其为准），结果确定、双方一致。
- 参数必须落在双方能力的**共同范围**内（码率交集 + 帧率/采样率白名单）。
- 协商失败只丢弃本轮 pending，**不前进版本，保留旧会话与旧轨道**。
- 迟到答复（`base < 当前版本`）与重复乱序消息返回 `stale`，绝不回退新版本。
- 停用轨道：从独立轨道存储删除并停止模拟源，释放对应状态。
- 断开：发/收消息在进程内管道缓存；恢复后重发，双方通过权威快照最终一致。

## 运行

```bash
go test ./...          # 单测：交集、冲突、失败保留、迟到答复、停用释放
go run ./cmd/demo      # 交叉消息 / 协商失败 / 断开恢复 演示
```

## 后续接入真实传输

`transport.Link` 替换为 WebRTC/Signaling 通道即可，消息体仍是 `ConfigureRequest`/
`ConfigureReply`/`SessionSnapshot`；`MediaRuntime.pump` 替换为真实 RTP 收发，
协商内核与存储无需改动。
