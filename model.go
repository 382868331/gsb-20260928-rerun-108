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
	Enabled     bool     `json:"enabled"`
	Direction   string   `json:"direction,omitempty"`
	Codecs      []string `json:"codecs,omitempty"`
	BitrateMin  int      `json:"bitrate_min,omitempty"`
	BitrateMax  int      `json:"bitrate_max,omitempty"`
	SampleRates []int    `json:"sample_rates,omitempty"`
	Channels    []int    `json:"channels,omitempty"`
	Resolutions []Size   `json:"resolutions,omitempty"`
}

type Peer struct {
	ID           string                     `json:"id"`
	Capabilities map[string]TrackCapability `json:"capabilities"`
}

type TrackState struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Direction   string   `json:"direction"`
	Codec       string   `json:"codec"`
	BitrateBps  int      `json:"bitrate_bps"`
	SampleRate  int      `json:"sample_rate,omitempty"`
	Channels    int      `json:"channels,omitempty"`
	Resolution  Size     `json:"resolution,omitempty"`
	Revision    int      `json:"revision"`
	Publisher   string   `json:"publisher,omitempty"`
	Subscribers []string `json:"subscribers,omitempty"`
}

type Session struct {
	ID       string       `json:"id"`
	Peers    []string     `json:"peers"`
	Tracks   []TrackState `json:"tracks"`
	Revision int          `json:"revision"`
	Scope    []string     `json:"scope,omitempty"`
}

type VersionStatus string

const (
	VersionProposing  VersionStatus = "proposing"
	VersionPartial    VersionStatus = "partial"
	VersionAccepted   VersionStatus = "accepted"
	VersionRejected   VersionStatus = "rejected"
	VersionSuperseded VersionStatus = "superseded"
)

type NegotiationVersion struct {
	SessionID  string          `json:"session_id"`
	Revision   int             `json:"revision"`
	Initiator  string          `json:"initiator"`
	Status     VersionStatus   `json:"status"`
	Reason     string          `json:"reason,omitempty"`
	Proposal   []TrackProposal `json:"proposal,omitempty"`
	Answer     []TrackProposal `json:"answer,omitempty"`
	Pending    []string        `json:"pending,omitempty"`
	AcceptedBy []string        `json:"accepted_by,omitempty"`
	Declined   []string        `json:"declined,omitempty"`
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
