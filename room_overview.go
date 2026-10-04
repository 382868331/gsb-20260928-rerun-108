package main

import "sort"

type VisibleTrack struct {
	ID              string      `json:"id"`
	Kind            string      `json:"kind"`
	OwnerPID        string      `json:"owner_pid"`
	OwnerName       string      `json:"owner_name"`
	OwnerGeneration int         `json:"owner_generation"`
	Params          MediaParams `json:"params"`
}

type AdjustmentView struct {
	ID           int          `json:"id"`
	BasedOn      int          `json:"based_on"`
	InitiatorPID string       `json:"initiator_pid"`
	Status       AdjustStatus `json:"status"`
	Items        []ItemView   `json:"items"`
}

type ItemView struct {
	Index      int               `json:"index"`
	Kind       ItemKind          `json:"kind"`
	TrackID    string            `json:"track_id"`
	TrackGen   int               `json:"track_generation"`
	TargetPID  string            `json:"target_pid"`
	Status     ItemStatus        `json:"status"`
	Active     MediaParams       `json:"active,omitempty"`
	Rejections map[string]string `json:"rejections,omitempty"`
}

type Overview struct {
	RoomID      string           `json:"room_id"`
	SelfPID     string           `json:"self_pid"`
	Revision    int              `json:"revision"`
	Present     []Participation  `json:"present"`
	Published   []VisibleTrack   `json:"published"`
	Visible     []VisibleTrack   `json:"visible"`
	Adjustments []AdjustmentView `json:"adjustments"`
}

// OverviewFor 只返回当前参与身份可以看到的内容：他人发布且该参与身份持有
// active 订阅的轨道才有媒体；概况同时列出尚待答复和明确拒绝的逐项范围。
func (r *RoomService) OverviewFor(roomID, pid string) (*Overview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, err := r.requirePresent(roomID, pid)
	if err != nil {
		return nil, err
	}
	view := &Overview{RoomID: roomID, SelfPID: pid, Revision: room.Revision}
	pids := room.sortedPIDs()
	for _, id := range pids {
		participant := room.Participations[id]
		if participant.Status == ParticipationPresent {
			view.Present = append(view.Present, *participant)
		}
	}
	trackIDs := make([]string, 0, len(room.TrackHistory))
	for id := range room.TrackHistory {
		trackIDs = append(trackIDs, id)
	}
	sort.Strings(trackIDs)
	for _, id := range trackIDs {
		track := room.CurrentTrack(id)
		if track == nil {
			continue
		}
		visible := VisibleTrack{
			ID:              track.ID,
			Kind:            track.Kind,
			OwnerPID:        track.OwnerPID,
			OwnerName:       track.OwnerName,
			OwnerGeneration: track.OwnerGeneration,
			Params:          track.Params,
		}
		view.Published = append(view.Published, visible)
		sub := room.activeSub(id, track.OwnerGeneration, pid)
		if sub != nil && sub.Status == SubActive {
			received := visible
			received.Params = sub.Params
			view.Visible = append(view.Visible, received)
		}
	}
	adjustIDs := make([]int, 0, len(room.Adjustments))
	for id := range room.Adjustments {
		adjustIDs = append(adjustIDs, id)
	}
	sort.Ints(adjustIDs)
	for _, id := range adjustIDs {
		adjust := room.Adjustments[id]
		relevant := false
		itemView := AdjustmentView{ID: adjust.ID, BasedOn: adjust.BasedOn, InitiatorPID: adjust.InitiatorPID, Status: adjust.Status}
		for i, item := range adjust.Items {
			if item.TargetPID != pid && adjust.InitiatorPID != pid {
				continue
			}
			relevant = true
			track := room.trackByGen(item.TrackID, item.TrackGen)
			status := item.Status
			if track == nil || !track.Published {
				if status == ItemPending {
					status = ItemCancelled
				}
			}
			entry := ItemView{
				Index:      i,
				Kind:       item.Kind,
				TrackID:    item.TrackID,
				TrackGen:   item.TrackGen,
				TargetPID:  item.TargetPID,
				Status:     status,
				Active:     item.Active,
				Rejections: item.Rejections,
			}
			itemView.Items = append(itemView.Items, entry)
		}
		if relevant {
			view.Adjustments = append(view.Adjustments, itemView)
		}
	}
	return view, nil
}
