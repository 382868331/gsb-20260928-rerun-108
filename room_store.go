package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func (s *Store) listRooms() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "room-") && strings.HasSuffix(name, ".json") {
			ids = append(ids, strings.TrimSuffix(strings.TrimPrefix(name, "room-"), ".json"))
		}
	}
	return ids, nil
}

func (s *Store) LoadRoom(id string) (*Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := "room-" + id + ".json"
	data, err := os.ReadFile(filepath.Join(s.root, name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var room Room
	if err := json.Unmarshal(data, &room); err != nil {
		return nil, err
	}
	return &room, nil
}

func (s *Store) SaveRoom(room *Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := "room-" + room.ID + ".json"
	return s.save(name, room)
}
