package main

import (
	"fmt"
	"sync"
)

var (
	ErrStale       = fmt.Errorf("stale negotiation response")
	ErrBusy        = fmt.Errorf("negotiation already in progress")
	ErrSuperseded  = fmt.Errorf("offer superseded by fixed conflict rule")
	ErrParticipant = fmt.Errorf("peer is not a session participant")
)

type Service struct {
	store    *Store
	mu       sync.Mutex
	peers    map[string]Peer
	sessions map[string]*Session
	versions map[string]map[int]*NegotiationVersion
}

func NewService(store *Store) (*Service, error) {
	sessions, err := store.LoadSessions()
	if err != nil {
		return nil, err
	}
	tracks, err := store.LoadTracks()
	if err != nil {
		return nil, err
	}
	versions, err := store.LoadVersions()
	if err != nil {
		return nil, err
	}
	for id, session := range sessions {
		session.Tracks = tracks[id]
		if session.Tracks == nil {
			session.Tracks = []TrackState{}
		}
	}
	return &Service{
		store:    store,
		peers:    map[string]Peer{},
		sessions: sessions,
		versions: versions,
	}, nil
}

func (s *Service) RegisterPeer(peer Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[peer.ID] = peer
}

func (s *Service) CreateSession(id string, peerA, peerB string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, exists := s.sessions[id]; exists {
		peers := sortedPeers([]string{peerA, peerB})
		if len(existing.Peers) == 2 && existing.Peers[0] == peers[0] && existing.Peers[1] == peers[1] {
			return cloneSession(existing), nil
		}
		return nil, fmt.Errorf("session %s already exists", id)
	}
	peers := sortedPeers([]string{peerA, peerB})
	if len(peers) != 2 || peers[0] == peers[1] {
		return nil, fmt.Errorf("session requires two distinct peers")
	}
	session := &Session{ID: id, Peers: peers, Tracks: []TrackState{}, Revision: 0, Scope: append([]string(nil), peers...)}
	s.sessions[id] = session
	version := &NegotiationVersion{
		SessionID: id,
		Revision:  0,
		Initiator: "system",
		Status:    VersionAccepted,
		Reason:    "initial empty session",
	}
	if s.versions[id] == nil {
		s.versions[id] = map[int]*NegotiationVersion{}
	}
	s.versions[id][0] = version
	if err := s.store.SaveSessionAndTracks(session); err != nil {
		return nil, err
	}
	if err := s.store.SaveVersion(version); err != nil {
		return nil, err
	}
	return cloneSession(session), nil
}

func (s *Service) JoinSession(sessionID, peerID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	if _, registered := s.peers[peerID]; !registered {
		return nil, fmt.Errorf("peer %s is not registered", peerID)
	}
	if peerIndex(session.Peers, peerID) >= 0 {
		return cloneSession(session), nil
	}
	session.Peers = sortedPeers(append(session.Peers, peerID))
	session.Scope = sortedPeers(append(append([]string(nil), session.Scope...), peerID))
	if err := s.store.SaveSessionAndTracks(session); err != nil {
		return nil, err
	}
	version := s.recordSystemVersion(sessionID, fmt.Sprintf("%s joined the session", peerID))
	if err := s.store.SaveVersion(version); err != nil {
		return nil, err
	}
	return cloneSession(session), nil
}

func (s *Service) LeaveSession(sessionID, peerID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, err := s.requireParticipant(sessionID, peerID)
	if err != nil {
		return nil, err
	}
	session.Peers = removeString(session.Peers, peerID)
	session.Scope = removeString(session.Scope, peerID)
	kept := make([]TrackState, 0, len(session.Tracks))
	for _, track := range session.Tracks {
		if track.Publisher == peerID {
			continue
		}
		track.Subscribers = removeString(track.Subscribers, peerID)
		kept = append(kept, track)
	}
	session.Tracks = kept
	for _, version := range s.versions[sessionID] {
		if version.Status != VersionProposing && version.Status != VersionPartial {
			continue
		}
		if version.Initiator == peerID {
			version.Status = VersionSuperseded
			version.Reason = fmt.Sprintf("initiator %s left the session", peerID)
		} else {
			version.Pending = removeString(version.Pending, peerID)
			s.finalizeVersion(version)
		}
		if err := s.store.SaveVersion(version); err != nil {
			return nil, err
		}
	}
	if err := s.store.SaveSessionAndTracks(session); err != nil {
		return nil, err
	}
	system := s.recordSystemVersion(sessionID, fmt.Sprintf("%s left the session", peerID))
	if err := s.store.SaveVersion(system); err != nil {
		return nil, err
	}
	return cloneSession(session), nil
}

func (s *Service) Session(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}
	return cloneSession(session), nil
}

func (s *Service) SyncView(sessionID, peerID string) (*Session, []TrackState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, err := s.requireParticipant(sessionID, peerID)
	if err != nil {
		return nil, nil, err
	}
	view := []TrackState{}
	for _, track := range session.Tracks {
		if track.Publisher == peerID || containsString(track.Subscribers, peerID) {
			view = append(view, track)
		}
	}
	return cloneSession(session), view, nil
}

func (s *Service) Version(sessionID string, revision int) (*NegotiationVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	version, ok := s.versions[sessionID][revision]
	if !ok {
		return nil, fmt.Errorf("version %d not found", revision)
	}
	return cloneVersion(version), nil
}

func (s *Service) Versions(sessionID string) ([]NegotiationVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	out := []NegotiationVersion{}
	for _, version := range s.versions[sessionID] {
		out = append(out, *cloneVersion(version))
	}
	sortVersions(out)
	return out, nil
}

func (s *Service) CreateOffer(sessionID, initiator string, proposal []TrackProposal) (*NegotiationVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, err := s.requireParticipant(sessionID, initiator)
	if err != nil {
		return nil, err
	}
	if err := validateProposalAgainstCapabilities(s.peers, session.Peers, proposal); err != nil {
		return nil, err
	}
	pending := s.pendingOffer(sessionID)
	version := &NegotiationVersion{
		SessionID:  sessionID,
		Revision:   s.nextRevision(sessionID),
		Initiator:  initiator,
		Status:     VersionProposing,
		Proposal:   cloneProposals(proposal),
		Pending:    removeString(append([]string(nil), session.Peers...), initiator),
		AcceptedBy: []string{initiator},
	}
	if s.versions[sessionID] == nil {
		s.versions[sessionID] = map[int]*NegotiationVersion{}
	}
	s.versions[sessionID][version.Revision] = version
	if pending != nil {
		if pending.Initiator == initiator && pending.Status == VersionProposing {
			return nil, fmt.Errorf("%w: revision %d", ErrBusy, pending.Revision)
		}
		if pending.Initiator == initiator {
			pending.Status = VersionSuperseded
			pending.Reason = fmt.Sprintf("initiator revised own offer with r%d", version.Revision)
			if err := s.store.SaveVersion(pending); err != nil {
				return nil, err
			}
			if err := s.store.SaveVersion(version); err != nil {
				return nil, err
			}
			return cloneVersion(version), nil
		}
		if pending.Status == VersionPartial {
			pending.Status = VersionSuperseded
			pending.Reason = fmt.Sprintf("superseded by newer offer r%d from %s", version.Revision, initiator)
			if err := s.store.SaveVersion(pending); err != nil {
				return nil, err
			}
			if err := s.store.SaveVersion(version); err != nil {
				return nil, err
			}
			return cloneVersion(version), nil
		}
		if initiator > pending.Initiator {
			version.Status = VersionSuperseded
			version.Reason = fmt.Sprintf("fixed rule: %s keeps precedence over %s", pending.Initiator, initiator)
			if err := s.store.SaveVersion(version); err != nil {
				return nil, err
			}
			return cloneVersion(version), nil
		}
		pending.Status = VersionSuperseded
		pending.Reason = fmt.Sprintf("fixed rule: %s wins conflict with %s", initiator, pending.Initiator)
		if err := s.store.SaveVersion(pending); err != nil {
			return nil, err
		}
	}
	if err := s.store.SaveVersion(version); err != nil {
		return nil, err
	}
	return cloneVersion(version), nil
}

func (s *Service) AnswerOffer(sessionID, responder string, revision int, answer []TrackProposal) (*Session, *NegotiationVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, err := s.requireParticipant(sessionID, responder)
	if err != nil {
		return nil, nil, err
	}
	versions := s.versions[sessionID]
	version, ok := versions[revision]
	if !ok {
		return nil, nil, fmt.Errorf("version %d not found", revision)
	}
	if version.Initiator == responder {
		return nil, nil, fmt.Errorf("offer initiator cannot answer its own offer")
	}
	if version.Status != VersionProposing && version.Status != VersionPartial {
		return cloneSession(session), cloneVersion(version), ErrStale
	}
	if !containsString(version.Pending, responder) {
		return cloneSession(session), cloneVersion(version), ErrStale
	}
	negotiated, err := negotiateTracks(s.peers, version.Initiator, responder, version.Proposal, answer, session.Tracks, session.Peers)
	version.Pending = removeString(version.Pending, responder)
	if err != nil {
		version.Declined = sortedPeers(append(version.Declined, responder))
		version.Reason = fmt.Sprintf("%s declined: %s", responder, err.Error())
		s.finalizeVersion(version)
		if saveErr := s.store.SaveVersion(version); saveErr != nil {
			return nil, nil, saveErr
		}
		return cloneSession(session), cloneVersion(version), err
	}
	for i := range negotiated {
		negotiated[i].Revision = revision
	}
	previous := append([]TrackState(nil), session.Tracks...)
	session.Tracks = negotiated
	session.Revision = revision
	version.AcceptedBy = sortedPeers(append(version.AcceptedBy, responder))
	session.Scope = append([]string(nil), version.AcceptedBy...)
	if err := s.store.SaveSessionAndTracks(session); err != nil {
		session.Tracks = previous
		return nil, nil, err
	}
	version.Answer = cloneProposals(answer)
	s.finalizeVersion(version)
	if err := s.store.SaveVersion(version); err != nil {
		return nil, nil, err
	}
	return cloneSession(session), cloneVersion(version), nil
}

func (s *Service) AdjustSubscription(sessionID, peerID, trackID string, subscribe bool) (*Session, *NegotiationVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, err := s.requireParticipant(sessionID, peerID)
	if err != nil {
		return nil, nil, err
	}
	index := -1
	for i, track := range session.Tracks {
		if track.ID == trackID {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, nil, fmt.Errorf("track %s is not active in session %s", trackID, sessionID)
	}
	if subscribe {
		if !containsString(session.Tracks[index].Subscribers, peerID) {
			session.Tracks[index].Subscribers = sortedPeers(append(session.Tracks[index].Subscribers, peerID))
		}
	} else {
		session.Tracks[index].Subscribers = removeString(session.Tracks[index].Subscribers, peerID)
	}
	if err := s.store.SaveSessionAndTracks(session); err != nil {
		return nil, nil, err
	}
	action := "subscribe"
	if !subscribe {
		action = "unsubscribe"
	}
	version := &NegotiationVersion{
		SessionID:  sessionID,
		Revision:   s.nextRevision(sessionID),
		Initiator:  peerID,
		Status:     VersionAccepted,
		Reason:     fmt.Sprintf("%s %s track %s", peerID, action, trackID),
		AcceptedBy: []string{peerID},
	}
	if s.versions[sessionID] == nil {
		s.versions[sessionID] = map[int]*NegotiationVersion{}
	}
	s.versions[sessionID][version.Revision] = version
	if err := s.store.SaveVersion(version); err != nil {
		return nil, nil, err
	}
	return cloneSession(session), cloneVersion(version), nil
}

func (s *Service) finalizeVersion(version *NegotiationVersion) {
	if version.Status != VersionProposing && version.Status != VersionPartial {
		return
	}
	if len(version.Pending) > 0 {
		version.Status = VersionPartial
		return
	}
	if len(version.AcceptedBy) <= 1 {
		version.Status = VersionRejected
		return
	}
	if len(version.Declined) > 0 {
		version.Status = VersionPartial
		return
	}
	version.Status = VersionAccepted
}

func (s *Service) recordSystemVersion(sessionID, reason string) *NegotiationVersion {
	version := &NegotiationVersion{
		SessionID: sessionID,
		Revision:  s.nextRevision(sessionID),
		Initiator: "system",
		Status:    VersionAccepted,
		Reason:    reason,
	}
	if s.versions[sessionID] == nil {
		s.versions[sessionID] = map[int]*NegotiationVersion{}
	}
	s.versions[sessionID][version.Revision] = version
	return version
}

func (s *Service) requireParticipant(sessionID, peerID string) (*Session, error) {
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	if peerIndex(session.Peers, peerID) < 0 {
		return nil, ErrParticipant
	}
	return session, nil
}

func (s *Service) nextRevision(sessionID string) int {
	max := 0
	for revision := range s.versions[sessionID] {
		if revision > max {
			max = revision
		}
	}
	return max + 1
}

func (s *Service) pendingOffer(sessionID string) *NegotiationVersion {
	for _, version := range s.versions[sessionID] {
		if version.Status == VersionProposing || version.Status == VersionPartial {
			return version
		}
	}
	return nil
}

func validateProposalAgainstCapabilities(peers map[string]Peer, participantIDs []string, proposal []TrackProposal) error {
	seen := map[string]bool{}
	for _, item := range proposal {
		if item.ID == "" || item.Kind == "" {
			return fmt.Errorf("track id and kind are required")
		}
		if seen[item.ID] {
			return fmt.Errorf("duplicate track id %s", item.ID)
		}
		seen[item.ID] = true
		for _, peerID := range participantIDs {
			capability, ok := peers[peerID].Capabilities[item.ID]
			if !ok {
				return fmt.Errorf("track %s is outside peer %s capability set", item.ID, peerID)
			}
			if capability.Kind != item.Kind {
				return fmt.Errorf("track %s kind is outside peer %s capability set", item.ID, peerID)
			}
		}
	}
	return nil
}

func cloneProposals(items []TrackProposal) []TrackProposal {
	if items == nil {
		return nil
	}
	out := make([]TrackProposal, len(items))
	copy(out, items)
	return out
}

func cloneVersion(version *NegotiationVersion) *NegotiationVersion {
	if version == nil {
		return nil
	}
	copy := *version
	copy.Proposal = cloneProposals(version.Proposal)
	copy.Answer = cloneProposals(version.Answer)
	copy.Pending = append([]string(nil), version.Pending...)
	copy.AcceptedBy = append([]string(nil), version.AcceptedBy...)
	copy.Declined = append([]string(nil), version.Declined...)
	return &copy
}

func cloneSession(session *Session) *Session {
	copy := *session
	copy.Peers = append([]string(nil), session.Peers...)
	copy.Tracks = append([]TrackState(nil), session.Tracks...)
	copy.Scope = append([]string(nil), session.Scope...)
	return &copy
}

func containsString(items []string, id string) bool {
	return peerIndex(items, id) >= 0
}

func removeString(items []string, id string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item != id {
			out = append(out, item)
		}
	}
	return out
}

func sortVersions(versions []NegotiationVersion) {
	for i := 1; i < len(versions); i++ {
		for j := i; j > 0 && versions[j].Revision < versions[j-1].Revision; j-- {
			versions[j], versions[j-1] = versions[j-1], versions[j]
		}
	}
}
