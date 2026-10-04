package main

type Direction string

const (
	DirectionSendRecv Direction = "sendrecv"
	DirectionSendOnly Direction = "sendonly"
	DirectionRecvOnly Direction = "recvonly"
	DirectionInactive Direction = "inactive"
)

type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type TrackCapability struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Directions  []string `json:"directions"`
	Codecs      []string `json:"codecs"`
	BitrateMin  int      `json:"bitrate_min"`
	BitrateMax  int      `json:"bitrate_max"`
	SampleRates []int    `json:"sample_rates,omitempty"`
	Channels    []int    `json:"channels,omitempty"`
	Resolutions []Size   `json:"resolutions,omitempty"`
}

type TrackProposal struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Owner       string   `json:"owner,omitempty"`
	SourceID    string   `json:"source_id,omitempty"`
	Enabled     bool     `json:"enabled"`
	Direction   string   `json:"direction,omitempty"`
	Codecs      []string `json:"codecs,omitempty"`
	BitrateMin  int      `json:"bitrate_min,omitempty"`
	BitrateMax  int      `json:"bitrate_max,omitempty"`
	SampleRates []int    `json:"sample_rates,omitempty"`
	Channels    []int    `json:"channels,omitempty"`
	Resolutions []Size   `json:"resolutions,omitempty"`
	Subscribers []string `json:"subscribers,omitempty"`
}

type Peer struct {
	ID           string                     `json:"id"`
	Capabilities map[string]TrackCapability `json:"capabilities"`
}

type TrackState struct {
	ID          string   `json:"id"`
	SourceID    string   `json:"source_id"`
	Kind        string   `json:"kind"`
	Publisher   string   `json:"publisher"`
	Direction   string   `json:"direction"`
	Codec       string   `json:"codec"`
	BitrateBps  int      `json:"bitrate_bps"`
	SampleRate  int      `json:"sample_rate,omitempty"`
	Channels    int      `json:"channels,omitempty"`
	Resolution  Size     `json:"resolution,omitempty"`
	Subscribers []string `json:"subscribers,omitempty"`
	Revision    int      `json:"revision"`
}

type Session struct {
	ID       string       `json:"id"`
	Peers    []string     `json:"peers"`
	Tracks   []TrackState `json:"tracks"`
	Revision int          `json:"revision"`
	Participants  []Participation `json:"participants,omitempty"`
	NextParticipantSeq int         `json:"next_participant_seq,omitempty"`
}

type Participation struct {
	ParticipationID string `json:"participation_id"`
	PersonID        string `json:"person_id"`
	DisplayName     string `json:"display_name"`
	Online          bool   `json:"online"`
	Present         bool   `json:"present"`
}

type VersionStatus string

const (
	VersionProposing  VersionStatus = "proposing"
	VersionAccepted   VersionStatus = "accepted"
	VersionPartial    VersionStatus = "partially_accepted"
	VersionRejected   VersionStatus = "rejected"
	VersionSuperseded VersionStatus = "superseded"
)

type VersionKind string

const (
	VersionKindOffer      VersionKind = "offer"
	VersionKindMembership VersionKind = "membership"
)

const (
	TrackPending  = "pending"
	TrackAccepted = "accepted"
	TrackRejected = "rejected"
	TrackWithdrawn = "withdrawn"
)

type PeerResponse struct {
	Status      VersionStatus     `json:"status"`
	Reason      string            `json:"reason,omitempty"`
	Answer      []TrackProposal   `json:"answer,omitempty"`
	TrackStatus map[string]string `json:"track_status,omitempty"`
}

type NegotiationVersion struct {
	SessionID string          `json:"session_id"`
	Revision  int             `json:"revision"`
	Kind      VersionKind     `json:"kind"`
	BasedOn   int             `json:"based_on"`
	Initiator string          `json:"initiator"`
	Status    VersionStatus   `json:"status"`
	Reason    string          `json:"reason,omitempty"`
	Participants []string        `json:"participants,omitempty"`
	Targets      []string        `json:"targets,omitempty"`
	Withdrawn    []string        `json:"withdrawn,omitempty"`
	Responses map[string]PeerResponse `json:"responses,omitempty"`
	Baseline  []TrackState    `json:"baseline,omitempty"`
	Proposal  []TrackProposal `json:"proposal,omitempty"`
	Answer    []TrackProposal `json:"answer,omitempty"`
}

type MessageType string

const (
	MessageOffer  MessageType = "offer"
	MessageAnswer MessageType = "answer"
	MessageNotice MessageType = "notice"
)

type Message struct {
	ID        string          `json:"id"`
	Type      MessageType     `json:"type"`
	SessionID string          `json:"session_id"`
	From      string          `json:"from"`
	To        string          `json:"to"`
	Revision  int             `json:"revision,omitempty"`
	Payload   []TrackProposal `json:"payload,omitempty"`
	Text      string          `json:"text,omitempty"`
}
