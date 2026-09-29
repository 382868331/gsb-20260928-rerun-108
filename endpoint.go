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

func (e *Endpoint) Handle(message Message) {
	sessionID := message.SessionID
	switch message.Type {
	case MessageOffer:
		version, err := e.service.Version(sessionID, message.Revision)
		if err == nil && version.Status != VersionProposing {
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
			fmt.Printf("r%d 协商失败，保留旧会话：%s\n", message.Revision, err)
			return
		}
		fmt.Printf("r%d 协商成功，%s 生效轨道：%s\n", version.Revision, session.ID, describeTracks(session.Tracks))
	case MessageNotice:
		fmt.Printf("%s 收到通知：%s\n", e.peerID, message.Text)
	}
}

func localAnswer(offer []TrackProposal) []TrackProposal {
	answer := make([]TrackProposal, len(offer))
	copy(answer, offer)
	return answer
}
