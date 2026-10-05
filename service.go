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
	ErrSelfAnswer  = fmt.Errorf("offer initiator cannot answer its own offer")
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

// AddParticipant creates a new participation generation for a display name.
// The first generation keeps the bare person id (legacy two-party addressing);
// a later re-entry by the same person gets a fresh identity such as
// "carol#2" instead of reusing the previous connection identity.
func (s *Service) AddParticipant(sessionID, personID, displayName string) (*Participation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	maxGeneration := 0
	for _, participation := range session.Participants {
		if participation.PersonID == personID && participation.Generation > maxGeneration {
			maxGeneration = participation.Generation
		}
	}
	for _, participation := range session.Participants {
		if participation.PersonID == personID && participation.Present {
			return nil, fmt.Errorf("person %s already present as %s", personID, participation.ParticipationID)
		}
	}
	generation := maxGeneration + 1
	participationID := personID
	if generation > 1 {
		participationID = fmt.Sprintf("%s#%d", personID, generation)
	}
	if session.NextParticipantSeq < generation {
		session.NextParticipantSeq = generation
	}
	participation := Participation{
		ParticipationID: participationID,
		PersonID:        personID,
		Generation:      generation,
		DisplayName:     displayName,
		Online:          true,
		Present:         true,
	}
	session.Participants = append(session.Participants, participation)
	if peerIndex(session.Peers, personID) < 0 {
		session.Peers = append(session.Peers, personID)
		sort.Strings(session.Peers)
	}
	if err := s.store.SaveSession(session); err != nil {
		return nil, err
	}
	result := participation
	return &result, nil
}

// BootstrapMultiParty turns the legacy two-party session into a generation
// tracked multi-party session by admitting its founding persons as
// generation 1. Existing two-party data stays untouched otherwise.
func (s *Service) BootstrapMultiParty(sessionID string, persons []string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	if len(session.Participants) > 0 {
		return cloneSession(session), nil
	}
	ordered := append([]string(nil), persons...)
	sort.Strings(ordered)
	for _, personID := range ordered {
		if peerIndex(session.Peers, personID) < 0 {
			return nil, fmt.Errorf("%w: %s", ErrParticipant, personID)
		}
		session.Participants = append(session.Participants, Participation{
			ParticipationID: personID,
			PersonID:        personID,
			Generation:      1,
			DisplayName:     personID,
			Online:          true,
			Present:         true,
		})
	}
	session.NextParticipantSeq = 1
	if err := s.store.SaveSession(session); err != nil {
		return nil, err
	}
	return cloneSession(session), nil
}

// LeaveParticipant ends the current generation of a person. The identity is
// retained historically (Present=false) so old decisions remain auditable,
// but its subscriptions and owned tracks stop being active immediately.
func (s *Service) LeaveParticipant(sessionID, participationID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	found := false
	for i := range session.Participants {
		if session.Participants[i].ParticipationID == participationID {
			session.Participants[i].Present = false
			session.Participants[i].Online = false
			found = true
			break
		}
	}
	if !found {
		return nil, ErrParticipant
	}
	// Any offer still waiting on the departed identity's publisher side is
	// withdrawn: a late consent must not resurrect its tracks.
	for _, version := range s.versions[sessionID] {
		if version.Status == VersionProposing && version.Initiator == participationID {
			version.Status = VersionSuperseded
			version.Reason = fmt.Sprintf("initiator %s left before agreement", participationID)
			if err := s.store.SaveVersion(version); err != nil {
				return nil, err
			}
		}
	}
	session.Tracks = pruneTracks(session.Tracks, presentSet(session), map[string]bool{participationID: true})
	if err := s.store.SaveSessionAndTracks(session); err != nil {
		return nil, err
	}
	return cloneSession(session), nil
}

func presentSet(session *Session) map[string]bool {
	out := map[string]bool{}
	for _, participation := range session.Participants {
		if participation.Present {
			out[participation.ParticipationID] = true
		}
	}
	return out
}

// pruneTracks drops tracks owned by withdrawn identities and removes absent or
// removed participants from subscriber lists. Tracks with no remaining
// subscriber disappear, releasing their state.
func pruneTracks(tracks []TrackState, present map[string]bool, removed map[string]bool) []TrackState {
	out := make([]TrackState, 0, len(tracks))
	for _, track := range tracks {
		if removed[track.Owner] || !present[track.Owner] {
			continue
		}
		kept := make([]string, 0, len(track.Subscribers))
		for _, subscriber := range track.Subscribers {
			if !removed[subscriber] && present[subscriber] {
				kept = append(kept, subscriber)
			}
		}
		if len(kept) == 0 {
			continue
		}
		track.Subscribers = kept
		out = append(out, track)
	}
	return out
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
	session := &Session{ID: id, Peers: peers, Tracks: []TrackState{}, Revision: 0}
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
	session, identity, err := s.requireActiveParticipant(sessionID, initiator)
	if err != nil {
		return nil, err
	}
	proposal = normalizeProposal(cloneProposals(proposal), identity)
	if err := validateProposalAgainstCapabilities(s.peers, session.Peers, proposal); err != nil {
		return nil, err
	}
	targets := make([]string, 0)
	for _, participationID := range orderedPresent(session) {
		if participationID != identity {
			targets = append(targets, participationID)
		}
	}
	pending := s.pendingOffer(sessionID)
	version := &NegotiationVersion{
		SessionID:    sessionID,
		Revision:     s.nextRevision(sessionID),
		Kind:         VersionKindOffer,
		Initiator:    identity,
		Status:       VersionProposing,
		Participants: orderedPresent(session),
		Targets:      targets,
		Responses:    map[string]PeerResponse{},
		Baseline:     append([]TrackState(nil), session.Tracks...),
		Proposal:     proposal,
	}
	if s.versions[sessionID] == nil {
		s.versions[sessionID] = map[int]*NegotiationVersion{}
	}
	s.versions[sessionID][version.Revision] = version
	if pending != nil {
		if pending.Initiator == initiator {
			return nil, fmt.Errorf("%w: revision %d", ErrBusy, pending.Revision)
		}
		if identity > pending.Initiator {
			version.Status = VersionSuperseded
			version.Reason = fmt.Sprintf("fixed rule: %s keeps precedence over %s", pending.Initiator, identity)
			if err := s.store.SaveVersion(version); err != nil {
				return nil, err
			}
			return cloneVersion(version), nil
		}
		pending.Status = VersionSuperseded
		pending.Reason = fmt.Sprintf("fixed rule: %s wins conflict with %s", identity, pending.Initiator)
		if err := s.store.SaveVersion(pending); err != nil {
			return nil, err
		}
	}
	if err := s.store.SaveVersion(version); err != nil {
		return nil, err
	}
	return cloneVersion(version), nil
}

// normalizeProposal tags every track with the owner generation and a stable
// source id (defaults to the track id), and defaults the subscriber list to
// all other current participants.
func normalizeProposal(proposal []TrackProposal, ownerID string) []TrackProposal {
	for i := range proposal {
		proposal[i].Owner = ownerID
		if proposal[i].SourceID == "" {
			proposal[i].SourceID = proposal[i].ID
		}
	}
	return proposal
}

func orderedPresent(session *Session) []string {
	ids := make([]string, 0)
	for _, participation := range session.Participants {
		if participation.Present {
			ids = append(ids, participation.ParticipationID)
		}
	}
	sort.Strings(ids)
	return ids
}

// requireActiveParticipant accepts either a person id (legacy two-party API)
// or an exact participation id and returns the current generation id.
func (s *Service) requireActiveParticipant(sessionID, who string) (*Session, string, error) {
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, "", fmt.Errorf("session %s not found", sessionID)
	}
	if len(session.Participants) == 0 {
		if peerIndex(session.Peers, who) < 0 {
			return nil, "", ErrParticipant
		}
		return session, who, nil
	}
	for _, participation := range session.Participants {
		if !participation.Present {
			continue
		}
		if participation.ParticipationID == who || participation.PersonID == who {
			return session, participation.ParticipationID, nil
		}
	}
	return nil, "", ErrParticipant
}

// AnswerOffer records one participant's response. In the legacy two-party
// session the single answer atomically replaces the session tracks. In a
// multi-party session each responder decides per track; the session keeps
// only tracks accepted by participants that are still in their accepted
// generation, so a partial set of answers yields a live partial session.
func (s *Service) AnswerOffer(sessionID, responder string, revision int, answer []TrackProposal) (*Session, *NegotiationVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, identity, err := s.requireActiveParticipant(sessionID, responder)
	if err != nil {
		// A departed generation answering a version it was targeted by is a
		// late consent: it is recorded for audit but never applied, and it
		// cannot be forwarded to a same-name new generation.
		if goneSession, goneID, gone := s.historicalParticipant(sessionID, responder); gone {
			if version, exists := s.versions[sessionID][revision]; exists {
				for _, target := range version.Targets {
					if target == goneID {
						version.Duplicates = append(version.Duplicates, DuplicateAnswer{
							Responder:  goneID,
							AtRevision: revision,
							Reason:     "late answer after participation ended",
							Answer:     cloneProposals(answer),
						})
						_ = s.store.SaveVersion(version)
						return cloneSession(goneSession), cloneVersion(version), ErrParticipant
					}
				}
			}
		}
		return nil, nil, err
	}
	versions := s.versions[sessionID]
	version, ok := versions[revision]
	if !ok {
		return nil, nil, fmt.Errorf("version %d not found", revision)
	}
	if version.Initiator == identity {
		return nil, nil, ErrSelfAnswer
	}
	multiParty := len(session.Participants) > 0
	// Only the exact target generation captured in the offer may answer it. A
	// same-name re-entry is a different identity and must not complete the old
	// generation's consent.
	if multiParty {
		targeted := false
		for _, target := range version.Targets {
			if target == identity {
				targeted = true
				break
			}
		}
		if !targeted {
			current := *version
			return cloneSession(session), &current, ErrParticipant
		}
	}
	// Multi-party versions keep collecting responses while partially
	// accepted; a responder answering twice is a stored duplicate. A
	// two-party version is closed by its single response, so any later
	// answer there is stale.
	collecting := version.Status == VersionProposing ||
		(multiParty && version.Status == VersionPartial)
	_, alreadyAnswered := version.Responses[identity]
	if !collecting || alreadyAnswered {
		version.Duplicates = append(version.Duplicates, DuplicateAnswer{
			Responder:  identity,
			AtRevision: revision,
			Reason:     version.Status.String(),
			Answer:     cloneProposals(answer),
		})
		if saveErr := s.store.SaveVersion(version); saveErr != nil {
			return nil, nil, saveErr
		}
		return cloneSession(session), cloneVersion(version), ErrStale
	}
	negotiated, err := negotiatePairSet(s.peers, personOf(session, version.Initiator), personOf(session, identity), version.Proposal, answer, revision)
	if err != nil {
		if !multiParty {
			version.Status = VersionRejected
			version.Answer = cloneProposals(answer)
			version.Reason = err.Error()
			if saveErr := s.store.SaveVersion(version); saveErr != nil {
				return nil, nil, saveErr
			}
			return cloneSession(session), cloneVersion(version), err
		}
		// A bad multi-party answer rejects that responder only; it never tears
		// down tracks other participants already accepted.
		version.Responses[identity] = PeerResponse{
			Status: VersionRejected,
			Reason: err.Error(),
			Answer: cloneProposals(answer),
		}
		version.Status = partialOrAccepted(version, session)
		if saveErr := s.store.SaveVersion(version); saveErr != nil {
			return nil, nil, saveErr
		}
		return cloneSession(session), cloneVersion(version), err
	}
	if !multiParty {
		previous := append([]TrackState(nil), session.Tracks...)
		session.Tracks = negotiated
		session.Revision = revision
		if err := s.store.SaveSessionAndTracks(session); err != nil {
			session.Tracks = previous
			return nil, nil, err
		}
		version.Status = VersionAccepted
		version.Answer = cloneProposals(answer)
		if err := s.store.SaveVersion(version); err != nil {
			return nil, nil, err
		}
		return cloneSession(session), cloneVersion(version), nil
	}

	// The responder must mention every offered track. An enabled answer is an
	// explicit acceptance; a disabled or missing answer declines the track.
	trackStatus := map[string]string{}
	// Answers are stored with the offer's owner generation so later replays
	// (after same-name re-entries) keep owner and subscriber identities in
	// the same participation-generation namespace.
	for i := range answer {
		answer[i].Owner = version.Initiator
		if answer[i].SourceID == "" {
			answer[i].SourceID = answer[i].ID
		}
	}
	answerIndex := proposalIndex(answer)
	for _, offered := range version.Proposal {
		if answered, ok := answerIndex[offered.ID]; ok && answered.Enabled && offered.Enabled {
			trackStatus[offered.ID] = TrackAccepted
		} else {
			trackStatus[offered.ID] = TrackRejected
		}
	}
	version.Responses[identity] = PeerResponse{
		Status:      VersionAccepted,
		Answer:      cloneProposals(answer),
		TrackStatus: trackStatus,
	}
	// Replay all still-valid responses: consents from departed generations
	// are ignored, and a same-name re-entry cannot answer for an old identity.
	session.Tracks = replayMultiPartyTracks(s.peers, version, session, revision)
	session.Revision = revision
	if err := s.store.SaveSessionAndTracks(session); err != nil {
		return nil, nil, err
	}
	version.Status = partialOrAccepted(version, session)
	version.Answer = cloneProposals(answer)
	if err := s.store.SaveVersion(version); err != nil {
		return nil, nil, err
	}
	return cloneSession(session), cloneVersion(version), nil
}

func (s VersionStatus) String() string { return string(s) }

// partialOrAccepted accepts a version only when every target that is still in
// its original generation has responded. A new generation never fills the gap
// left by a departed identity, so consents from different generations can
// never be stitched into one overall acceptance.
func partialOrAccepted(version *NegotiationVersion, session *Session) VersionStatus {
	present := presentSet(session)
	validTargets := 0
	decided := 0
	for _, target := range version.Targets {
		if !present[target] {
			continue
		}
		validTargets++
		if _, ok := version.Responses[target]; ok {
			decided++
		}
	}
	if validTargets > 0 && decided == validTargets {
		return VersionAccepted
	}
	return VersionPartial
}

// replayMultiPartyTracks rebuilds active tracks from scratch by replaying
// every valid per-responder answer. The first accepted responder's negotiated
// parameters define the shared transport state; later accepted responders
// join the subscriber list. Baseline tracks from other owners survive.
func replayMultiPartyTracks(peers map[string]Peer, version *NegotiationVersion, session *Session, revision int) []TrackState {
	present := presentSet(session)
	initiatorPerson := personOf(session, version.Initiator)
	result := []TrackState{}
	for _, offered := range version.Proposal {
		if !offered.Enabled {
			continue
		}
		var state TrackState
		subscribers := []string{}
		responderIDs := make([]string, 0, len(version.Responses))
		for id := range version.Responses {
			responderIDs = append(responderIDs, id)
		}
		sort.Strings(responderIDs)
		for _, responderID := range responderIDs {
			response := version.Responses[responderID]
			if response.TrackStatus[offered.ID] != TrackAccepted || !present[responderID] {
				continue
			}
			answered, ok := proposalIndex(response.Answer)[offered.ID]
			if !ok {
				continue
			}
			negotiated, err := negotiatePair(offered, peers[initiatorPerson], peers[personOf(session, responderID)], answered, revision)
			if err != nil {
				continue
			}
			if len(subscribers) == 0 {
				state = negotiated
			}
			subscribers = append(subscribers, responderID)
		}
		if len(subscribers) > 0 {
			state.Subscribers = subscribers
			result = append(result, state)
		}
	}
	for _, track := range version.Baseline {
		if track.Owner == version.Initiator {
			continue
		}
		kept := []string{}
		for _, subscriber := range track.Subscribers {
			if present[subscriber] {
				kept = append(kept, subscriber)
			}
		}
		if present[track.Owner] && len(kept) > 0 {
			track.Subscribers = kept
			result = append(result, track)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Owner != result[j].Owner {
			return result[i].Owner < result[j].Owner
		}
		return result[i].ID < result[j].ID
	})
	return result
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
		if version.Status == VersionProposing {
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
	return &copy
}

func cloneSession(session *Session) *Session {
	copy := *session
	copy.Peers = append([]string(nil), session.Peers...)
	copy.Tracks = append([]TrackState(nil), session.Tracks...)
	copy.Participants = append([]Participation(nil), session.Participants...)
	return &copy
}

// personOf maps a participation generation id back to the stable person id
// used as the peer/capability registry key.
func personOf(session *Session, participationID string) string {
	for _, participation := range session.Participants {
		if participation.ParticipationID == participationID {
			return participation.PersonID
		}
	}
	return participationID
}

// historicalParticipant looks up an ended participation generation (by
// participation id or person id) for recording late, non-applied answers.
func (s *Service) historicalParticipant(sessionID, who string) (*Session, string, bool) {
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, "", false
	}
	for _, participation := range session.Participants {
		if participation.Present {
			continue
		}
		if participation.ParticipationID == who || participation.PersonID == who {
			return session, participation.ParticipationID, true
		}
	}
	return nil, "", false
}
