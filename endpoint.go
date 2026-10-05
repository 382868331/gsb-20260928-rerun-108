package main

import (
	"fmt"
	"sort"
)

type Endpoint struct {
	sessionID string
	peerID    string
	service   *Service
	bus       *Bus
	accept    func(TrackProposal) bool
}

func NewEndpoint(sessionID, peerID string, service *Service, bus *Bus) *Endpoint {
	endpoint := &Endpoint{sessionID: sessionID, peerID: peerID, service: service, bus: bus}
	bus.Register(peerID, endpoint.Handle)
	return endpoint
}

// SetAcceptPolicy installs a per-track acceptance predicate. Tracks the
// predicate rejects are answered as disabled, so they never become visible
// to this endpoint even though other participants may accept them.
func (e *Endpoint) SetAcceptPolicy(accept func(TrackProposal) bool) {
	e.accept = accept
}

func (e *Endpoint) ID() string { return e.peerID }

func (e *Endpoint) Connect() {
	e.bus.Connect(e.peerID)
}

func (e *Endpoint) Disconnect() {
	e.bus.Disconnect(e.peerID)
}

// Close ends this participation's connection identity: its bus address and
// queued messages are discarded. A later re-entry gets a fresh endpoint id.
func (e *Endpoint) Close() {
	e.bus.Unregister(e.peerID)
}

// VisibleTracks returns exactly the tracks this endpoint explicitly accepted
// and whose owner is still in the same participation generation.
func (e *Endpoint) VisibleTracks() []TrackState {
	session, err := e.service.Session(e.sessionID)
	if err != nil {
		return nil
	}
	visible := make([]TrackState, 0)
	for _, track := range session.Tracks {
		if track.Owner == e.peerID {
			visible = append(visible, track)
			continue
		}
		for _, subscriber := range track.Subscribers {
			if subscriber == e.peerID {
				visible = append(visible, track)
				break
			}
		}
	}
	return visible
}

func (e *Endpoint) Offer(target string, proposal []TrackProposal) (*NegotiationVersion, error) {
	return e.OfferTargets([]string{target}, proposal)
}

// OfferTargets creates an offer addressed to specific participation
// generations. Only listed targets count toward acceptance; absent members
// stay absent until a later offer.
func (e *Endpoint) OfferTargets(targets []string, proposal []TrackProposal) (*NegotiationVersion, error) {
	version, err := e.service.CreateOffer(e.sessionID, e.peerID, proposal)
	if err != nil {
		return nil, err
	}
	if version.Status == VersionSuperseded {
		return version, nil
	}
	for _, target := range targets {
		if err := e.bus.Send(Message{
			Type:      MessageOffer,
			SessionID: e.sessionID,
			From:      version.Initiator,
			To:        target,
			Revision:  version.Revision,
			Payload:   proposal,
		}); err != nil {
			return version, err
		}
	}
	return version, nil
}

func (e *Endpoint) Handle(message Message) {
	sessionID := message.SessionID
	switch message.Type {
	case MessageOffer:
		version, err := e.service.Version(sessionID, message.Revision)
		if err == nil && (version.Status == VersionSuperseded || version.Status == VersionAccepted || version.Status == VersionRejected) {
			reason := version.Reason
			if version.Status == VersionAccepted {
				reason = "offer already fully accepted"
			}
			fmt.Printf("%s 忽略 %s 的旧提案 r%d：%s（%s）\n", e.peerID, message.From, message.Revision, version.Status, reason)
			return
		}
		_ = err
		// An offer from a generation that no longer exists must not be acted
		// upon by a same-name re-entry: check that the initiator is current.
		if !e.initiatorPresent(version) {
			fmt.Printf("%s 丢弃 %s 的旧身份消息 r%d：发起世代已不存在\n", e.peerID, message.From, message.Revision)
			return
		}
		answer := e.localAnswer(message.Payload)
		session, version, err := e.service.AnswerOffer(sessionID, e.peerID, message.Revision, answer)
		if err == ErrStale {
			fmt.Printf("%s 的迟到答复 r%d 被拒绝，当前仍是 r%d\n", e.peerID, message.Revision, session.Revision)
			return
		}
		if err == ErrParticipant {
			fmt.Printf("%s 的旧身份答复 r%d 无效：该参与世代已离场\n", e.peerID, message.Revision)
			return
		}
		if err != nil {
			fmt.Printf("r%d %s 答复记录为拒绝：%s\n", message.Revision, e.peerID, err)
			return
		}
		fmt.Printf("r%d %s 答复后状态=%s，%s 可见来源：%s\n", version.Revision, e.peerID, version.Status, e.peerID, describeTracks(e.VisibleTracks()))
	case MessageNotice:
		fmt.Printf("%s 收到通知：%s\n", e.peerID, message.Text)
	}
}

func (e *Endpoint) initiatorPresent(version *NegotiationVersion) bool {
	if version == nil {
		return true
	}
	session, err := e.service.Session(e.sessionID)
	if err != nil {
		return false
	}
	for _, participation := range session.Participants {
		if participation.ParticipationID == version.Initiator && participation.Present {
			return true
		}
	}
	return len(session.Participants) == 0
}

func (e *Endpoint) localAnswer(offer []TrackProposal) []TrackProposal {
	answer := make([]TrackProposal, len(offer))
	for i, item := range offer {
		if e.accept != nil && !e.accept(item) {
			item.Enabled = false
		}
		answer[i] = item
	}
	sort.SliceStable(answer, func(i, j int) bool { return answer[i].ID < answer[j].ID })
	return answer
}
