package main

import "fmt"

type Endpoint struct {
	sessionID string
	peerID    string
	service   *Service
	bus       *Bus
}

func NewEndpoint(sessionID, peerID string, service *Service, bus *Bus) *Endpoint {
	endpoint := &Endpoint{sessionID: sessionID, peerID: peerID, service: service, bus: bus}
	bus.Register(peerID, endpoint.Handle)
	return endpoint
}

func (e *Endpoint) ID() string { return e.peerID }

func (e *Endpoint) Connect() {
	e.bus.Connect(e.peerID)
}

func (e *Endpoint) Disconnect() {
	e.bus.Disconnect(e.peerID)
}

func (e *Endpoint) Offer(target string, proposal []TrackProposal) (*NegotiationVersion, error) {
	version, err := e.service.CreateOffer(e.sessionID, e.peerID, proposal)
	if err != nil {
		return nil, err
	}
	if version.Status == VersionSuperseded {
		return version, nil
	}
	err = e.bus.Send(Message{
		Type:      MessageOffer,
		SessionID: e.sessionID,
		From:      e.peerID,
		To:        target,
		Revision:  version.Revision,
		Payload:   proposal,
	})
	return version, err
}

func (e *Endpoint) OfferAll(proposal []TrackProposal) (*NegotiationVersion, error) {
	version, err := e.service.CreateOffer(e.sessionID, e.peerID, proposal)
	if err != nil {
		return nil, err
	}
	if version.Status == VersionSuperseded {
		return version, nil
	}
	session, err := e.service.Session(e.sessionID)
	if err != nil {
		return nil, err
	}
	for _, peer := range session.Peers {
		if peer == e.peerID {
			continue
		}
		if err := e.bus.Send(Message{
			Type:      MessageOffer,
			SessionID: e.sessionID,
			From:      e.peerID,
			To:        peer,
			Revision:  version.Revision,
			Payload:   proposal,
		}); err != nil {
			return version, err
		}
	}
	return version, nil
}

func (e *Endpoint) Sync() {
	session, tracks, err := e.service.SyncView(e.sessionID, e.peerID)
	if err != nil {
		fmt.Printf("%s 追平失败：%s\n", e.peerID, err)
		return
	}
	fmt.Printf("%s 追平到 r%d（生效范围 %s），可参与轨道：%s\n",
		e.peerID, session.Revision, joinPeers(session.Scope), describeTracks(tracks))
}

func (e *Endpoint) Handle(message Message) {
	sessionID := message.SessionID
	switch message.Type {
	case MessageOffer:
		version, err := e.service.Version(sessionID, message.Revision)
		if err == nil && version.Status != VersionProposing && version.Status != VersionPartial {
			fmt.Printf("%s 忽略 %s 的旧提案 r%d：%s\n", e.peerID, message.From, message.Revision, version.Reason)
			return
		}
		answer := localAnswer(message.Payload)
		session, version, err := e.service.AnswerOffer(sessionID, e.peerID, message.Revision, answer)
		if err == ErrStale {
			fmt.Printf("%s 的迟到答复 r%d 被拒绝，当前仍是 r%d\n", e.peerID, message.Revision, session.Revision)
			return
		}
		if err != nil {
			fmt.Printf("r%d 对 %s 未生效，其余范围保留：%s\n", message.Revision, e.peerID, err)
			return
		}
		fmt.Printf("r%d 状态=%s，%s 生效范围：%s；轨道：%s\n",
			version.Revision, version.Status, session.ID, joinPeers(session.Scope), describeTracks(session.Tracks))
		if len(version.Pending) > 0 {
			fmt.Printf("r%d 尚未全体确认，待确认：%s\n", version.Revision, joinPeers(version.Pending))
		}
	case MessageNotice:
		fmt.Printf("%s 收到通知：%s\n", e.peerID, message.Text)
	}
}

func joinPeers(peers []string) string {
	if len(peers) == 0 {
		return "(空)"
	}
	out := peers[0]
	for _, peer := range peers[1:] {
		out += "," + peer
	}
	return out
}

func localAnswer(offer []TrackProposal) []TrackProposal {
	answer := make([]TrackProposal, len(offer))
	copy(answer, offer)
	return answer
}
