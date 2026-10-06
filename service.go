package main

import (
	"fmt"
	"sort"
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

// CreateSession creates a two-party session; both peers become the first
// participation generation addressed by their peer ID.
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
	session := &Session{ID: id, Peers: peers, Tracks: []TrackState{}, Revision: 0, NextParticipantSeq: 2}
	for i, peerID := range peers {
		session.Participants = append(session.Participants, Participation{
			ParticipationID: peerID,
			PersonID:        peerID,
			DisplayName:     peerID,
			Online:          true,
			Present:         true,
		})
		_ = i
	}
	s.sessions[id] = session
	version := &NegotiationVersion{
		SessionID: id,
		Revision:  0,
		Kind:      VersionKindMembership,
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

// JoinSession adds a new participation generation for a person. Rejoining
// with the same person ID never reuses the old participation identity: a
// suffixed generation ID (person#2, person#3, ...) is minted instead.
func (s *Service) JoinSession(sessionID, personID, displayName string) (*Session, Participation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, Participation{}, fmt.Errorf("session %s not found", sessionID)
	}
	if _, ok := s.peers[personID]; !ok {
		return nil, Participation{}, fmt.Errorf("unknown peer %s", personID)
	}
	generation := 0
	for _, p := range session.Participants {
		if p.PersonID == personID {
			generation++
		}
		if p.PersonID == personID && p.Present {
			return nil, Participation{}, fmt.Errorf("person %s already has a live participation %s", personID, p.ParticipationID)
		}
	}
	participationID := personID
	if generation > 0 {
		participationID = fmt.Sprintf("%s#%d", personID, generation+1)
	}
	if displayName == "" {
		displayName = personID
	}
	participant := Participation{
		ParticipationID: participationID,
		PersonID:        personID,
		DisplayName:     displayName,
		Online:          true,
		Present:         true,
	}
	session.Participants = append(session.Participants, participant)
	session.NextParticipantSeq++
	if peerIndex(session.Peers, personID) < 0 {
		session.Peers = sortedPeers(append(session.Peers, personID))
	}
	if err := s.store.SaveSessionAndTracks(session); err != nil {
		return nil, Participation{}, err
	}
	return cloneSession(session), participant, nil
}

// LeaveSession removes a participation generation, withdraws the tracks it
// published, drops it from every subscriber list, and re-evaluates any
// pending offer so a departed target can neither block nor complete it.
func (s *Service) LeaveSession(sessionID, participationID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	idx := participationIndex(session.Participants, participationID)
	if idx < 0 || !session.Participants[idx].Present {
		return nil, ErrParticipant
	}
	session.Participants[idx].Present = false
	session.Participants[idx].Online = false

	kept := session.Tracks[:0]
	for _, track := range session.Tracks {
		if track.Owner == participationID {
			continue
		}
		track.Subscribers = removeString(track.Subscribers, participationID)
		kept = append(kept, track)
	}
	session.Tracks = kept

	revision := s.nextRevision(sessionID)
	session.Revision = revision
	version := &NegotiationVersion{
		SessionID: sessionID,
		Revision:  revision,
		Kind:      VersionKindMembership,
		BasedOn:   session.Revision,
		Initiator: "system",
		Status:    VersionAccepted,
		Reason:    fmt.Sprintf("participation %s left", participationID),
		Withdrawn: []string{participationID},
	}
	if s.versions[sessionID] == nil {
		s.versions[sessionID] = map[int]*NegotiationVersion{}
	}
	s.versions[sessionID][revision] = version
	if err := s.store.SaveSessionAndTracks(session); err != nil {
		return nil, err
	}
	if err := s.store.SaveVersion(version); err != nil {
		return nil, err
	}
	if pending := s.pendingOffer(sessionID); pending != nil {
		changed := false
		if removeStringInPlace(&pending.Targets, participationID) {
			changed = true
		}
		if changed {
			if err := s.evaluateOffer(session, pending); err != nil {
				return nil, err
			}
		}
	}
	return cloneSession(session), nil
}

// Participation returns a recorded participation generation, present or not.
func (s *Service) Participation(sessionID, participationID string) (Participation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return Participation{}, fmt.Errorf("session %s not found", sessionID)
	}
	idx := participationIndex(session.Participants, participationID)
	if idx < 0 {
		return Participation{}, ErrParticipant
	}
	return session.Participants[idx], nil
}

// SessionFor returns the view of a single present participation: only tracks
// it owns or explicitly accepted (is subscribed to) and that are still valid.
func (s *Service) SessionFor(sessionID, participationID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, err := s.requireParticipant(sessionID, participationID)
	if err != nil {
		return nil, err
	}
	view := cloneSession(session)
	tracks := make([]TrackState, 0, len(view.Tracks))
	for _, track := range view.Tracks {
		if track.Owner == participationID || containsString(track.Subscribers, participationID) {
			tracks = append(tracks, track)
		}
	}
	view.Tracks = tracks
	return view, nil
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

func (s *Service) Version(sessionID string, revision int) (*NegotiationVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	version, ok := s.versions[sessionID][revision]
	if !ok {
		return nil, fmt.Errorf("version %d not found", revision)
	}
	copy := *version
	return &copy, nil
}

func (s *Service) CreateOffer(sessionID, initiator string, proposal []TrackProposal) (*NegotiationVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, err := s.requireParticipant(sessionID, initiator)
	if err != nil {
		return nil, err
	}
	if err := validateProposalAgainstCapabilities(s.peers, s.presentPersons(session), proposal); err != nil {
		return nil, err
	}
	pending := s.pendingOffer(sessionID)
	targets := make([]string, 0, len(session.Participants))
	for _, p := range session.Participants {
		if p.Present && p.ParticipationID != initiator {
			targets = append(targets, p.ParticipationID)
		}
	}
	version := &NegotiationVersion{
		SessionID: sessionID,
		Revision:  s.nextRevision(sessionID),
		Kind:      VersionKindOffer,
		BasedOn:   session.Revision,
		Initiator: initiator,
		Status:    VersionProposing,
		Targets:   targets,
		Responses: map[string]PeerResponse{},
		Proposal:  cloneProposals(proposal),
	}
	if s.versions[sessionID] == nil {
		s.versions[sessionID] = map[int]*NegotiationVersion{}
	}
	s.versions[sessionID][version.Revision] = version
	if pending != nil {
		if pending.Initiator == initiator {
			return nil, fmt.Errorf("%w: revision %d", ErrBusy, pending.Revision)
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

// AnswerOffer records one participation's consent. Answers are keyed by
// participation generation: a removed or superseded identity cannot answer,
// a repeated answer from the same generation is an idempotent no-op, and a
// version is accepted only when every still-present target of this round has
// accepted — consent is never stitched across generations.
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
	if version.Kind != VersionKindOffer {
		return nil, nil, fmt.Errorf("version %d is not an offer", revision)
	}
	if version.Initiator == responder {
		return nil, nil, fmt.Errorf("offer initiator cannot answer its own offer")
	}
	if version.Status != VersionProposing && version.Status != VersionPartial {
		return cloneSession(session), cloneVersion(version), ErrStale
	}
	if !containsString(version.Targets, responder) {
		return nil, nil, fmt.Errorf("%w: %s is not a target of revision %d", ErrParticipant, responder, revision)
	}
	if version.Responses == nil {
		version.Responses = map[string]PeerResponse{}
	}
	if _, dup := version.Responses[responder]; dup {
		return cloneSession(session), cloneVersion(version), nil
	}
	answers, err := validateAnswerShape(version.Proposal, answer)
	if err != nil {
		version.Status = VersionRejected
		version.Reason = err.Error()
		if saveErr := s.store.SaveVersion(version); saveErr != nil {
			return nil, nil, saveErr
		}
		return nil, nil, err
	}
	response := PeerResponse{Status: VersionAccepted, Answer: cloneProposals(answer), TrackStatus: map[string]string{}}
	for id, answered := range answers {
		if answered.Enabled {
			response.TrackStatus[id] = TrackPending
		} else {
			response.TrackStatus[id] = TrackRejected
		}
	}
	version.Responses[responder] = response
	version.Status = VersionPartial
	if err := s.evaluateOffer(session, version); err != nil {
		return nil, nil, err
	}
	if version.Status == VersionRejected {
		return cloneSession(session), cloneVersion(version), fmt.Errorf("%s", version.Reason)
	}
	return cloneSession(session), cloneVersion(version), nil
}

// evaluateOffer finalizes an offer once every still-present target has
// answered. Negotiated tracks replace the initiator's previously published
// tracks; other owners' tracks are untouched.
func (s *Service) evaluateOffer(session *Session, version *NegotiationVersion) error {
	required := s.requiredTargets(session, version)
	for _, target := range required {
		if _, ok := version.Responses[target]; !ok {
			return s.store.SaveVersion(version)
		}
	}
	initiatorIdx := participationIndex(session.Participants, version.Initiator)
	if initiatorIdx < 0 {
		version.Status = VersionRejected
		version.Reason = fmt.Sprintf("initiator %s is no longer a participation", version.Initiator)
		return s.store.SaveVersion(version)
	}
	ownerPerson := session.Participants[initiatorIdx].PersonID
	ownerPeer, ok := s.peers[ownerPerson]
	if !ok {
		version.Status = VersionRejected
		version.Reason = fmt.Sprintf("unknown peer %s", ownerPerson)
		return s.store.SaveVersion(version)
	}

	offers := proposalIndex(version.Proposal)
	ids := make([]string, 0, len(offers))
	for id := range offers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	negotiated := make([]TrackState, 0, len(ids))
	enabledOffer := false
	enabledAnswer := false
	for _, id := range ids {
		offered := offers[id]
		if !offered.Enabled {
			for _, target := range required {
				markTrackStatus(version, target, id, TrackWithdrawn)
			}
			continue
		}
		enabledOffer = true
		subscribers := make([]string, 0, len(required))
		answers := map[string]TrackProposal{}
		answerers := map[string]Peer{}
		for _, target := range required {
			response := version.Responses[target]
			answered := proposalIndex(response.Answer)[id]
			if !answered.Enabled {
				markTrackStatus(version, target, id, TrackRejected)
				continue
			}
			enabledAnswer = true
			targetPerson := session.Participants[participationIndex(session.Participants, target)].PersonID
			subscribers = append(subscribers, target)
			answers[target] = answered
			answerers[target] = s.peers[targetPerson]
		}
		if len(subscribers) == 0 {
			continue
		}
		offered.Owner = version.Initiator
		state, err := negotiateTrack(offered, ownerPeer, answers, answerers, version.Revision)
		if err != nil {
			for _, target := range subscribers {
				markTrackStatus(version, target, id, TrackRejected)
			}
			continue
		}
		state.Owner = version.Initiator
		state.Publisher = version.Initiator
		state.SourceID = offered.SourceID
		if state.SourceID == "" {
			state.SourceID = version.Initiator + "/" + id
		}
		if len(offered.Subscribers) > 0 {
			filtered := make([]string, 0, len(subscribers))
			for _, sub := range subscribers {
				if containsString(offered.Subscribers, sub) {
					filtered = append(filtered, sub)
				} else {
					markTrackStatus(version, sub, id, TrackRejected)
				}
			}
			subscribers = filtered
		}
		if len(subscribers) == 0 {
			continue
		}
		state.Subscribers = subscribers
		for _, target := range subscribers {
			markTrackStatus(version, target, id, TrackAccepted)
		}
		negotiated = append(negotiated, state)
	}

	if len(negotiated) == 0 && enabledOffer && enabledAnswer {
		version.Status = VersionRejected
		version.Reason = "no track reached a common parameter range"
		return s.store.SaveVersion(version)
	}

	kept := make([]TrackState, 0, len(session.Tracks)+len(negotiated))
	for _, track := range session.Tracks {
		if track.Owner != version.Initiator {
			kept = append(kept, track)
		}
	}
	session.Tracks = append(kept, negotiated...)
	sort.Slice(session.Tracks, func(i, j int) bool {
		if session.Tracks[i].Owner != session.Tracks[j].Owner {
			return session.Tracks[i].Owner < session.Tracks[j].Owner
		}
		return session.Tracks[i].ID < session.Tracks[j].ID
	})
	previousRevision := session.Revision
	session.Revision = version.Revision
	if err := s.store.SaveSessionAndTracks(session); err != nil {
		session.Revision = previousRevision
		return err
	}
	version.Status = VersionAccepted
	return s.store.SaveVersion(version)
}

// requiredTargets returns the targets of an offer that are still present
// participation generations; only their consent counts toward acceptance.
func (s *Service) requiredTargets(session *Session, version *NegotiationVersion) []string {
	out := make([]string, 0, len(version.Targets))
	for _, target := range version.Targets {
		idx := participationIndex(session.Participants, target)
		if idx >= 0 && session.Participants[idx].Present {
			out = append(out, target)
		}
	}
	return out
}

func (s *Service) presentPersons(session *Session) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(session.Participants))
	for _, p := range session.Participants {
		if p.Present && !seen[p.PersonID] {
			seen[p.PersonID] = true
			out = append(out, p.PersonID)
		}
	}
	return out
}

func (s *Service) requireParticipant(sessionID, peerID string) (*Session, error) {
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	idx := participationIndex(session.Participants, peerID)
	if idx < 0 || !session.Participants[idx].Present {
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
		if version.Kind == VersionKindOffer && (version.Status == VersionProposing || version.Status == VersionPartial) {
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
	copy.Targets = append([]string(nil), version.Targets...)
	copy.Withdrawn = append([]string(nil), version.Withdrawn...)
	copy.Participants = append([]string(nil), version.Participants...)
	if version.Responses != nil {
		copy.Responses = make(map[string]PeerResponse, len(version.Responses))
		for k, v := range version.Responses {
			copy.Responses[k] = v
		}
	}
	return &copy
}

func cloneSession(session *Session) *Session {
	copy := *session
	copy.Peers = append([]string(nil), session.Peers...)
	copy.Tracks = append([]TrackState(nil), session.Tracks...)
	copy.Participants = append([]Participation(nil), session.Participants...)
	return &copy
}

func participationIndex(participants []Participation, id string) int {
	for i, p := range participants {
		if p.ParticipationID == id {
			return i
		}
	}
	return -1
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func removeString(items []string, value string) []string {
	out := items[:0]
	for _, item := range items {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}

func removeStringInPlace(items *[]string, value string) bool {
	before := len(*items)
	*items = removeString(*items, value)
	return len(*items) != before
}

func markTrackStatus(version *NegotiationVersion, responder, trackID, status string) {
	response, ok := version.Responses[responder]
	if !ok {
		return
	}
	if response.TrackStatus == nil {
		response.TrackStatus = map[string]string{}
	}
	response.TrackStatus[trackID] = status
	version.Responses[responder] = response
}
