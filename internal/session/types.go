package session

import "errors"

// Direction 描述一个端点在某条轨道上的期望方向。
type Direction int

const (
	DirInactive Direction = iota // 停用轨道，释放对应状态
	DirSendOnly
	DirRecvOnly
	DirSendRecv
)

func (d Direction) sends() bool    { return d == DirSendOnly || d == DirSendRecv }
func (d Direction) receives() bool { return d == DirRecvOnly || d == DirSendRecv }
func (d Direction) wants() bool    { return d != DirInactive }

type Kind int

const (
	KindAudio Kind = iota
	KindVideo
)

// ErrNegotiation 表示协商失败；失败时保留旧会话，不产生新版本。
var ErrNegotiation = errors.New("negotiation failed")

// Range 是闭区间 [Min, Max]。
type Range struct {
	Min int
	Max int
}

func (r Range) valid() bool { return r.Min > 0 && r.Max >= r.Min }

// Intersect 取双方共同范围；无交集时 ok=false。
func (r Range) Intersect(other Range) (out Range, ok bool) {
	out = Range{max(r.Min, other.Min), min(r.Max, other.Max)}
	return out, out.valid()
}

// MediaParams 是协商媒体参数。必须落在双方能力的共同范围内。
type MediaParams struct {
	BitrateBps Range // 期望码率区间，服务端在交集中取值
	Framerate  int   // 视频帧率，音频为 0
	SampleRate int   // 音频采样率，视频为 0
}

// TrackSpec 是单个端点对一条轨道的期望配置。
type TrackSpec struct {
	Kind      Kind
	Direction Direction
	Params    MediaParams
}

// Capability 是一个端点支持的媒体能力范围。
type Capability struct {
	Name        string
	Bitrate     Range
	FrameRates  []int // 可接受的视频帧率集合
	SampleRates []int // 可接受的音频采样率集合
}

func (c Capability) supports(k Kind, p MediaParams) bool {
	// 期望码率必须完全落在本端能力范围内。
	if p.BitrateBps.Min < c.Bitrate.Min || p.BitrateBps.Max > c.Bitrate.Max {
		return false
	}
	switch k {
	case KindVideo:
		return p.Framerate > 0 && contains(c.FrameRates, p.Framerate)
	case KindAudio:
		return p.SampleRate > 0 && contains(c.SampleRates, p.SampleRate)
	}
	return false
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// ActiveTrack 是协商成功后会话中实际生效的轨道。
type ActiveTrack struct {
	ID       string
	Kind     Kind
	Sender   string // 实际发送方（双向时按固定规则取较小端点 ID）
	Receiver string
	Params   MediaParams
}

// Intent 是一个端点提交的整份期望，带单调递增序号。
type Intent struct {
	SessionID string
	PeerID    string
	Seq       int64
	Base      int64 // 提交时看到的协商版本
	Tracks    map[string]TrackSpec
}
