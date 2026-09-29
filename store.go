package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type Store struct {
	root string
	mu   sync.Mutex
}

func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) load(name string, value any) error {
	data, err := os.ReadFile(filepath.Join(s.root, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, value)
}

func (s *Store) save(name string, value any) error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.root, name+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.root, name))
}

func (s *Store) LoadSessions() (map[string]*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*Session{}
	err := s.load("sessions.json", &out)
	return out, err
}

func (s *Store) SaveSession(session *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := func() (map[string]*Session, error) {
		out := map[string]*Session{}
		if err := s.load("sessions.json", &out); err != nil {
			return nil, err
		}
		out[session.ID] = session
		return out, nil
	}()
	if err != nil {
		return err
	}
	return s.save("sessions.json", sessions)
}

func (s *Store) LoadTracks() (map[string][]TrackState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]TrackState{}
	err := s.load("tracks.json", &out)
	return out, err
}

func (s *Store) SaveTracks(sessionID string, tracks []TrackState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := map[string][]TrackState{}
	if err := s.load("tracks.json", &all); err != nil {
		return err
	}
	if tracks == nil {
		tracks = []TrackState{}
	}
	all[sessionID] = tracks
	return s.save("tracks.json", all)
}

func (s *Store) LoadVersions() (map[string]map[int]*NegotiationVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[int]*NegotiationVersion{}
	err := s.load("versions.json", &out)
	return out, err
}

func (s *Store) SaveVersion(version *NegotiationVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := map[string]map[int]*NegotiationVersion{}
	if err := s.load("versions.json", &all); err != nil {
		return err
	}
	if all[version.SessionID] == nil {
		all[version.SessionID] = map[int]*NegotiationVersion{}
	}
	all[version.SessionID][version.Revision] = version
	return s.save("versions.json", all)
}

func (s *Store) SaveSessionAndTracks(session *Session) error {
	if err := s.SaveSession(session); err != nil {
		return err
	}
	return s.SaveTracks(session.ID, session.Tracks)
}

func sortedPeers(peers []string) []string {
	out := append([]string(nil), peers...)
	sort.Strings(out)
	return out
}

func peerIndex(peers []string, id string) int {
	for i, peer := range peers {
		if peer == id {
			return i
		}
	}
	return -1
}

func otherPeer(session *Session, id string) (string, error) {
	if len(session.Peers) != 2 {
		return "", fmt.Errorf("session %s is not a two-party session", session.ID)
	}
	if session.Peers[0] == id {
		return session.Peers[1], nil
	}
	if session.Peers[1] == id {
		return session.Peers[0], nil
	}
	return "", fmt.Errorf("peer %s is not in session %s", id, session.ID)
}
