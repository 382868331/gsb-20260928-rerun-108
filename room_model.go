package main

import "sort"

type ParticipationStatus string

const (
	ParticipationPresent ParticipationStatus = "present"
	ParticipationGone    ParticipationStatus = "gone"
)

// Participation 标识一次具体的加入。同名用户离开后再加入会得到不同的
// generation，因此旧连接上的迟到答复不可能冒充新连接。
type Participation struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Generation int                 `json:"generation"`
	Status     ParticipationStatus `json:"status"`
}

type MediaParams struct {
	Direction  string `json:"direction,omitempty"`
	Codec      string `json:"codec,omitempty"`
	BitrateBps int    `json:"bitrate_bps,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
	Channels   int    `json:"channels,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
}

// RTrack 是一条已发布轨道的一个来源世代。轨道标识被同名重入者重新使用时，
// OwnerGeneration 不同，订阅与媒体帧都按具体来源区分。
type RTrack struct {
	ID              string      `json:"id"`
	Kind            string      `json:"kind"`
	OwnerPID        string      `json:"owner_pid"`
	OwnerName       string      `json:"owner_name"`
	OwnerGeneration int         `json:"owner_generation"`
	Published       bool        `json:"published"`
	Params          MediaParams `json:"params"`
	PublishedRev    int         `json:"published_revision"`
	RemovedRev      int         `json:"removed_revision,omitempty"`
}

type SubscriptionStatus string

const (
	SubActive    SubscriptionStatus = "active"
	SubRejected  SubscriptionStatus = "rejected"
	SubWithdrawn SubscriptionStatus = "withdrawn"
)

// Subscription 是某个参与身份对某个轨道来源的最终决定，撤回后永久保留。
type Subscription struct {
	TrackID         string             `json:"track_id"`
	OwnerGeneration int                `json:"owner_generation"`
	Kind            string             `json:"kind"`
	SubscriberPID   string             `json:"subscriber_pid"`
	SubscriberName  string             `json:"subscriber_name"`
	SubscriberGen   int                `json:"subscriber_generation"`
	Status          SubscriptionStatus `json:"status"`
	Params          MediaParams        `json:"params"`
	Reason          string             `json:"reason,omitempty"`
	Revision        int                `json:"revision"`
}

type ItemKind string

const (
	ItemOffer       ItemKind = "offer"
	ItemSubscribe   ItemKind = "subscribe"
	ItemUpdate      ItemKind = "update"
	ItemRemove      ItemKind = "remove"
	ItemUnsubscribe ItemKind = "unsubscribe"
)

type ItemStatus string

const (
	ItemPending   ItemStatus = "pending"
	ItemAccepted  ItemStatus = "accepted"
	ItemRejected  ItemStatus = "rejected"
	ItemCancelled ItemStatus = "cancelled"
)

// AdjustItem 的决定按完整参与身份（name#generation）记录，互不合并。
type AdjustItem struct {
	Kind       ItemKind              `json:"kind"`
	TrackID    string                `json:"track_id"`
	TrackGen   int                   `json:"track_gen"`
	TargetPID  string                `json:"target_pid"`
	Status     ItemStatus            `json:"status"`
	Decisions  map[string]ItemStatus `json:"decisions,omitempty"`
	Rejections map[string]string     `json:"rejections,omitempty"`
	Offer      MediaParams           `json:"offer_params,omitempty"`
	Active     MediaParams           `json:"active_params,omitempty"`
}

type AdjustStatus string

const (
	AdjustPending  AdjustStatus = "pending"
	AdjustComplete AdjustStatus = "complete"
)

type Adjustment struct {
	ID           int          `json:"id"`
	BasedOn      int          `json:"based_on"`
	InitiatorPID string       `json:"initiator_pid"`
	Status       AdjustStatus `json:"status"`
	Items        []AdjustItem `json:"items"`
}

type Room struct {
	ID             string                    `json:"id"`
	Revision       int                       `json:"revision"`
	NextGeneration map[string]int            `json:"next_generation"`
	NextAdjustment int                       `json:"next_adjustment"`
	Participations map[string]*Participation `json:"participations"`
	PIDOrder       []string                  `json:"pid_order"`
	TrackHistory   map[string][]*RTrack      `json:"track_history"`
	Subscriptions  []*Subscription           `json:"subscriptions"`
	Adjustments    map[int]*Adjustment       `json:"adjustments"`
}

func makePID(name string, generation int) string {
	return name + "#" + itoa(generation)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func (r *Room) participation(pid string) *Participation {
	return r.Participations[pid]
}

func (r *Room) present(pid string) bool {
	p := r.Participations[pid]
	return p != nil && p.Status == ParticipationPresent
}

// CurrentTrack 返回某个轨道标识当前仍在发布的来源；被移除或被新来源替代
// 时旧世代不会被返回。
func (r *Room) CurrentTrack(id string) *RTrack {
	history := r.TrackHistory[id]
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Published {
			return history[i]
		}
	}
	return nil
}

func (r *Room) trackByGen(id string, generation int) *RTrack {
	for _, track := range r.TrackHistory[id] {
		if track.OwnerGeneration == generation {
			return track
		}
	}
	return nil
}

func (r *Room) activeSub(trackID string, ownerGen int, subscriberPID string) *Subscription {
	for i := len(r.Subscriptions) - 1; i >= 0; i-- {
		sub := r.Subscriptions[i]
		if sub.TrackID == trackID && sub.OwnerGeneration == ownerGen && sub.SubscriberPID == subscriberPID {
			return sub
		}
	}
	return nil
}

func (r *Room) sortedPIDs() []string {
	pids := make([]string, 0, len(r.Participations))
	for pid := range r.Participations {
		pids = append(pids, pid)
	}
	sort.Strings(pids)
	return pids
}
