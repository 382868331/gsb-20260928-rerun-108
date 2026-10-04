package main

import (
	"fmt"
	"sort"
	"sync"
)

var (
	ErrUnknownRoom       = fmt.Errorf("unknown room")
	ErrStaleParticipant  = fmt.Errorf("message belongs to a participation that has left")
	ErrUnknownAdjustment = fmt.Errorf("unknown adjustment")
	ErrNoDecisionNeeded  = fmt.Errorf("participation is not an addressee of this item")
)

type RoomService struct {
	store *Store
	mu    sync.Mutex
	cap   map[string]map[string]TrackCapability
	rooms map[string]*Room
}

func NewRoomService(store *Store) (*RoomService, error) {
	service := &RoomService{
		store: store,
		cap:   map[string]map[string]TrackCapability{},
		rooms: map[string]*Room{},
	}
	entries, err := store.listRooms()
	if err != nil {
		return nil, err
	}
	for _, id := range entries {
		room, err := store.LoadRoom(id)
		if err != nil {
			return nil, err
		}
		if room != nil {
			service.rooms[id] = room
		}
	}
	return service, nil
}

func (r *RoomService) RegisterCapabilities(name string, capabilities map[string]TrackCapability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cap[name] = capabilities
}

func (r *RoomService) ensureRoom(id string) *Room {
	room := r.rooms[id]
	if room == nil {
		room = &Room{
			ID:             id,
			NextGeneration: map[string]int{},
			Participations: map[string]*Participation{},
			TrackHistory:   map[string][]*RTrack{},
			Adjustments:    map[int]*Adjustment{},
		}
		r.rooms[id] = room
	}
	return room
}

// Join 返回新的参与身份。同名重入会得到递增 generation，旧 pid 立即失效。
func (r *RoomService) Join(roomID, name string) (*Participation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room := r.ensureRoom(roomID)
	if room.NextGeneration == nil {
		room.NextGeneration = map[string]int{}
	}
	room.NextGeneration[name]++
	generation := room.NextGeneration[name]
	pid := makePID(name, generation)
	participant := &Participation{ID: pid, Name: name, Generation: generation, Status: ParticipationPresent}
	room.Participations[pid] = participant
	room.PIDOrder = append(room.PIDOrder, pid)
	return participant, r.store.SaveRoom(room)
}

func (r *RoomService) Snapshot(roomID, pid string) (*Room, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[roomID]
	if !ok {
		return nil, ErrUnknownRoom
	}
	if !room.present(pid) {
		return nil, ErrStaleParticipant
	}
	return cloneRoom(room), nil
}

// Leave 保留已经做出的决定；只撤回该参与身份仍待处理或仍在接收的内容。
func (r *RoomService) Leave(roomID, pid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, err := r.requirePresent(roomID, pid)
	if err != nil {
		return err
	}
	room.Revision++
	participant := room.Participations[pid]
	participant.Status = ParticipationGone

	for _, history := range room.TrackHistory {
		for _, track := range history {
			if track.Published && track.OwnerPID == pid {
				track.Published = false
				track.RemovedRev = room.Revision
			}
		}
	}
	for _, sub := range room.Subscriptions {
		if !subActiveForLeave(room, sub, pid) {
			continue
		}
		sub.Status = SubWithdrawn
		sub.Reason = "participation left"
		sub.Revision = room.Revision
	}
	ids := make([]int, 0, len(room.Adjustments))
	for id := range room.Adjustments {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		adjust := room.Adjustments[id]
		if adjust.Status == AdjustComplete {
			continue
		}
		for i := range adjust.Items {
			item := &adjust.Items[i]
			if item.Status != ItemPending {
				continue
			}
			track := room.trackByGen(item.TrackID, item.TrackGen)
			trackGone := track == nil || !track.Published
			// 只停止只有离开者仍在场才可能完成的项；其他成员之间的独立
			// 协商继续进行。
			involvesLeaver := item.TargetPID == pid || (track != nil && track.OwnerPID == pid)
			if involvesLeaver || (trackGone && adjust.InitiatorPID == pid) {
				markItemCancelled(item, pid, room.Revision)
				if item.TargetPID == pid && (item.Kind == ItemOffer || item.Kind == ItemSubscribe || item.Kind == ItemUpdate) {
					r.rememberRejectionLocked(room, item, "participation left before answering")
				}
			}
		}
		recomputeAdjustStatus(adjust)
	}
	return r.store.SaveRoom(room)
}

func subActiveForLeave(room *Room, sub *Subscription, pid string) bool {
	if sub.Status != SubActive {
		return false
	}
	if sub.SubscriberPID == pid {
		return true
	}
	track := room.trackByGen(sub.TrackID, sub.OwnerGeneration)
	return track != nil && track.OwnerPID == pid
}

func markItemCancelled(item *AdjustItem, gonePID string, revision int) {
	if item.Status == ItemPending {
		item.Status = ItemCancelled
		if item.Decisions == nil {
			item.Decisions = map[string]ItemStatus{}
		}
		item.Decisions[gonePID] = ItemCancelled
	}
}

func recomputeAdjustStatus(adjust *Adjustment) {
	for i := range adjust.Items {
		item := &adjust.Items[i]
		if item.Status == ItemPending {
			return
		}
	}
	adjust.Status = AdjustComplete
}

func (r *RoomService) requirePresent(roomID, pid string) (*Room, error) {
	room, ok := r.rooms[roomID]
	if !ok {
		return nil, ErrUnknownRoom
	}
	if !room.present(pid) {
		return nil, ErrStaleParticipant
	}
	return room, nil
}

func (r *RoomService) persist(room *Room) error {
	return r.store.SaveRoom(room)
}

// PublishTrack 立即可见：其他参与者在概况中能看到该轨道来源，但只有被接受的
// 订阅才会收到帧。轨道 ID 复用且旧来源已移除时，会创建新的来源世代。
func (r *RoomService) PublishTrack(roomID, pid, id, kind string, params MediaParams) (*RTrack, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, err := r.requirePresent(roomID, pid)
	if err != nil {
		return nil, err
	}
	if current := room.CurrentTrack(id); current != nil {
		if current.OwnerPID == pid {
			return nil, fmt.Errorf("track %s is already published by %s", id, pid)
		}
		return nil, fmt.Errorf("track id %s is in use by another live participation", id)
	}
	participant := room.Participations[pid]
	if err := validateRoomParams(r.cap[participant.Name], id, kind, params); err != nil {
		return nil, err
	}
	room.Revision++
	track := &RTrack{
		ID:              id,
		Kind:            kind,
		OwnerPID:        pid,
		OwnerName:       participant.Name,
		OwnerGeneration: participant.Generation,
		Published:       true,
		Params:          params,
		PublishedRev:    room.Revision,
	}
	room.TrackHistory[id] = append(room.TrackHistory[id], track)
	if err := r.persist(room); err != nil {
		return nil, err
	}
	copy := *track
	return &copy, nil
}

// RemoveTrack 立即停止该来源；逐项记录每个订阅者已经生效或拒绝的范围。
func (r *RoomService) RemoveTrack(roomID, pid, id string) (*Adjustment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, err := r.requirePresent(roomID, pid)
	if err != nil {
		return nil, err
	}
	track := room.CurrentTrack(id)
	if track == nil {
		return nil, fmt.Errorf("track %s is not published", id)
	}
	if track.OwnerPID != pid {
		return nil, fmt.Errorf("only the publisher can remove track %s", id)
	}
	room.Revision++
	track.Published = false
	track.RemovedRev = room.Revision

	itemsByTarget := map[string]*AdjustItem{}
	for _, sub := range room.Subscriptions {
		if sub.TrackID != id || sub.OwnerGeneration != track.OwnerGeneration {
			continue
		}
		item := &AdjustItem{
			Kind:      ItemRemove,
			TrackID:   id,
			TrackGen:  track.OwnerGeneration,
			TargetPID: sub.SubscriberPID,
			Decisions: map[string]ItemStatus{},
		}
		if sub.Status == SubActive {
			sub.Status = SubWithdrawn
			sub.Reason = "track removed by publisher"
			sub.Revision = room.Revision
			item.Status = ItemAccepted
			item.Decisions[sub.SubscriberPID] = ItemAccepted
			itemsByTarget[sub.SubscriberPID] = item
		} else if sub.Status == SubRejected {
			item.Status = ItemRejected
			item.Decisions[sub.SubscriberPID] = ItemRejected
			if sub.Reason != "" {
				item.Rejections = map[string]string{sub.SubscriberPID: sub.Reason}
			}
			itemsByTarget[sub.SubscriberPID] = item
		}
	}
	var adjust *Adjustment
	if len(itemsByTarget) > 0 {
		targets := make([]string, 0, len(itemsByTarget))
		for target := range itemsByTarget {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		items := make([]AdjustItem, 0, len(targets))
		for _, target := range targets {
			items = append(items, *itemsByTarget[target])
		}
		adjust = r.newAdjustmentLocked(room, pid, items, true)
	}
	if err := r.persist(room); err != nil {
		return nil, err
	}
	if adjust == nil {
		return nil, nil
	}
	return cloneAdjustment(adjust), nil
}

// Unsubscribe 由接收者主动撤回；撤回决定永久保留，即使显示名称相同也不会
// 因为新的提案而被自动恢复。
func (r *RoomService) Unsubscribe(roomID, pid, id string, generation int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, err := r.requirePresent(roomID, pid)
	if err != nil {
		return err
	}
	track := room.trackByGen(id, generation)
	if track == nil {
		return fmt.Errorf("unknown track source %s#%d", id, generation)
	}
	room.Revision++
	sub := room.activeSub(id, generation, pid)
	if sub != nil && sub.Status == SubActive {
		sub.Status = SubWithdrawn
		sub.Reason = "unsubscribed by receiver"
		sub.Revision = room.Revision
	}
	for _, adjust := range room.Adjustments {
		if adjust.Status == AdjustComplete {
			continue
		}
		for i := range adjust.Items {
			item := &adjust.Items[i]
			if item.TrackID == id && item.TrackGen == generation && item.TargetPID == pid && item.Status == ItemPending {
				item.Status = ItemRejected
				if item.Decisions == nil {
					item.Decisions = map[string]ItemStatus{}
				}
				item.Decisions[pid] = ItemRejected
				if item.Rejections == nil {
					item.Rejections = map[string]string{}
				}
				item.Rejections[pid] = "unsubscribed before answering"
				r.rememberRejectionLocked(room, item, "unsubscribed before answering")
			}
		}
		recomputeAdjustStatus(adjust)
	}
	return r.persist(room)
}

// Offer 由发布者向一组接收参与身份发起订阅协商；每人一项，互不合并。
// 新加入者不会替已经缺席的同名旧参与身份接受旧提案。
func (r *RoomService) Offer(roomID, publisherPID, id string, params MediaParams, targetPIDs []string) (*Adjustment, error) {
	return r.propose(roomID, publisherPID, ItemOffer, id, params, targetPIDs)
}

// RequestSubscribe 由接收者在取得概况后显式订阅；不会因为显示名称相同
// 自动恢复旧参与身份撤回过的订阅。
func (r *RoomService) RequestSubscribe(roomID, subscriberPID, id string, params MediaParams) (*Adjustment, error) {
	return r.propose(roomID, subscriberPID, ItemSubscribe, id, params, nil)
}

// RequestUpdate 请求修改一条已经生效订阅的参数。
func (r *RoomService) RequestUpdate(roomID, requesterPID, id string, params MediaParams, otherPID string) (*Adjustment, error) {
	targets := []string{otherPID}
	return r.propose(roomID, requesterPID, ItemUpdate, id, params, targets)
}

func (r *RoomService) propose(roomID, requesterPID string, kind ItemKind, id string, params MediaParams, targetPIDs []string) (*Adjustment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, err := r.requirePresent(roomID, requesterPID)
	if err != nil {
		return nil, err
	}
	track := room.CurrentTrack(id)
	if track == nil {
		return nil, fmt.Errorf("track %s is not currently published", id)
	}
	var targets []string
	switch kind {
	case ItemOffer:
		targets = targetPIDs
	case ItemSubscribe:
		if track.OwnerPID == requesterPID {
			return nil, fmt.Errorf("publisher cannot subscribe to its own track")
		}
		targets = []string{track.OwnerPID}
	case ItemUpdate:
		targets = targetPIDs
	default:
		return nil, fmt.Errorf("unsupported proposal kind %s", kind)
	}
	items := make([]AdjustItem, 0, len(targets))
	for _, target := range targets {
		if !room.present(target) {
			return nil, fmt.Errorf("%w: %s", ErrStaleParticipant, target)
		}
		if kind == ItemOffer && target == requesterPID {
			continue
		}
		item := AdjustItem{
			Kind:      kind,
			TrackID:   id,
			TrackGen:  track.OwnerGeneration,
			TargetPID: target,
			Status:    ItemPending,
			Decisions: map[string]ItemStatus{},
			Offer:     params,
		}
		if kind == ItemUpdate {
			subscriberPID := updateSubscriber(track.OwnerPID, requesterPID, target)
			sub := room.activeSub(id, track.OwnerGeneration, subscriberPID)
			if sub == nil || sub.Status != SubActive {
				return nil, fmt.Errorf("track %s has no active subscription to update for %s", id, subscriberPID)
			}
			item.Active = sub.Params
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("proposal has no addressees")
	}
	adjust := r.newAdjustmentLocked(room, requesterPID, items, false)
	if err := r.persist(room); err != nil {
		return nil, err
	}
	return cloneAdjustment(adjust), nil
}

func updateSubscriber(ownerPID, requesterPID, target string) string {
	if requesterPID != ownerPID {
		return requesterPID
	}
	return target
}

// Respond 记录一个参与身份对某一项的明确决定。重复答复是幂等的：恢复保存
// 状态后重放同一答复不会再次修改协商或会话。
func (r *RoomService) Respond(roomID, pid string, adjustmentID, itemIndex int, accept bool, params MediaParams, reason string) (*Adjustment, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, err := r.requirePresent(roomID, pid)
	if err != nil {
		return nil, false, err
	}
	adjust := room.Adjustments[adjustmentID]
	if adjust == nil {
		return nil, false, ErrUnknownAdjustment
	}
	if itemIndex < 0 || itemIndex >= len(adjust.Items) {
		return nil, false, fmt.Errorf("item index %d out of range", itemIndex)
	}
	item := &adjust.Items[itemIndex]
	if item.TargetPID != pid {
		return nil, false, ErrNoDecisionNeeded
	}
	if _, already := item.Decisions[pid]; already {
		return cloneAdjustment(adjust), true, nil
	}
	if item.Status != ItemPending {
		return nil, false, ErrStale
	}
	track := room.trackByGen(item.TrackID, item.TrackGen)
	if track == nil || !track.Published {
		item.Status = ItemCancelled
		item.Decisions[pid] = ItemCancelled
		recomputeAdjustStatus(adjust)
		return nil, false, r.persist(room)
	}
	room.Revision++
	participant := room.Participations[pid]
	subscriberPID := pid
	if item.Kind == ItemSubscribe {
		subscriberPID = adjust.InitiatorPID
	} else if adjust.InitiatorPID != track.OwnerPID {
		subscriberPID = adjust.InitiatorPID
	}
	if item.Kind == ItemUpdate {
		subscriberPID = updateSubscriber(track.OwnerPID, adjust.InitiatorPID, pid)
	}
	subscriber := room.Participations[subscriberPID]
	if !accept {
		if reason == "" {
			reason = "rejected by participant"
		}
		item.Status = ItemRejected
		item.Decisions[pid] = ItemRejected
		if item.Rejections == nil {
			item.Rejections = map[string]string{}
		}
		item.Rejections[pid] = reason
		r.rememberRejectionLocked(room, item, reason)
	} else {
		negotiated, nerr := resolveRoomParams(
			track,
			r.cap[track.OwnerName],
			r.cap[subscriber.Name],
			item,
			params,
		)
		if nerr != nil {
			reason = nerr.Error()
			item.Status = ItemRejected
			item.Decisions[pid] = ItemRejected
			if item.Rejections == nil {
				item.Rejections = map[string]string{}
			}
			item.Rejections[pid] = reason
			r.rememberRejectionLocked(room, item, reason)
		} else {
			item.Status = ItemAccepted
			item.Decisions[pid] = ItemAccepted
			item.Active = negotiated
			r.applyAcceptedLocked(room, item, participant, subscriber, track, negotiated)
		}
	}
	recomputeAdjustStatus(adjust)
	if err := r.persist(room); err != nil {
		return nil, false, err
	}
	return cloneAdjustment(adjust), false, nil
}
