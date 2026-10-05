package main

import (
	"fmt"
	"sort"
)

func proposalIndex(items []TrackProposal) map[string]TrackProposal {
	out := map[string]TrackProposal{}
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func capabilityIndex(items []TrackCapability) map[string]TrackCapability {
	out := map[string]TrackCapability{}
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func sliceSet(items []string) map[string]bool {
	out := map[string]bool{}
	for _, item := range items {
		out[item] = true
	}
	return out
}

func intSet(items []int) map[int]bool {
	out := map[int]bool{}
	for _, item := range items {
		out[item] = true
	}
	return out
}

func sizeSet(items []Size) map[Size]bool {
	out := map[Size]bool{}
	for _, item := range items {
		out[item] = true
	}
	return out
}

func commonStrings(groups ...[]string) []string {
	if len(groups) == 0 {
		return nil
	}
	sets := make([]map[string]bool, 0, len(groups))
	for _, group := range groups {
		sets = append(sets, sliceSet(group))
	}
	out := make([]string, 0)
	for value := range sets[0] {
		ok := true
		for _, set := range sets[1:] {
			if !set[value] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func commonInts(groups ...[]int) []int {
	if len(groups) == 0 {
		return nil
	}
	sets := make([]map[int]bool, 0, len(groups))
	for _, group := range groups {
		sets = append(sets, intSet(group))
	}
	out := make([]int, 0)
	for value := range sets[0] {
		ok := true
		for _, set := range sets[1:] {
			if !set[value] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, value)
		}
	}
	sort.Ints(out)
	return out
}

func commonSizes(groups ...[]Size) []Size {
	if len(groups) == 0 {
		return nil
	}
	sets := make([]map[Size]bool, 0, len(groups))
	for _, group := range groups {
		sets = append(sets, sizeSet(group))
	}
	out := make([]Size, 0)
	for value := range sets[0] {
		ok := true
		for _, set := range sets[1:] {
			if !set[value] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].Width * out[i].Height
		right := out[j].Width * out[j].Height
		if left != right {
			return left < right
		}
		return out[i].Width < out[j].Width
	})
	return out
}

// negotiatePair computes the active track state for one track as accepted by
// one responder. Intersection is taken over the owner capability, the offer
// parameters, the responder capability and the responder answer parameters.
func negotiatePair(track TrackProposal, owner, responder Peer, answered TrackProposal, revision int) (TrackState, error) {
	id := track.ID
	offeredCap, ok := owner.Capabilities[id]
	if !ok {
		return TrackState{}, fmt.Errorf("track %s is unsupported by owner %s", id, owner.ID)
	}
	answeredCap, ok := responder.Capabilities[id]
	if !ok {
		return TrackState{}, fmt.Errorf("track %s is unsupported by responder %s", id, responder.ID)
	}
	if track.Kind != answered.Kind {
		return TrackState{}, fmt.Errorf("track %s kind mismatch", id)
	}
	directions := commonStrings(offeredCap.Directions, []string{track.Direction}, answeredCap.Directions, []string{answered.Direction})
	codecs := commonStrings(offeredCap.Codecs, track.Codecs, answeredCap.Codecs, answered.Codecs)
	minBitrate := maxInt(track.BitrateMin, offeredCap.BitrateMin, answered.BitrateMin, answeredCap.BitrateMin)
	maxBitrate := minInt(track.BitrateMax, offeredCap.BitrateMax, answered.BitrateMax, answeredCap.BitrateMax)
	if len(directions) == 0 {
		return TrackState{}, fmt.Errorf("track %s has no common direction", id)
	}
	if len(codecs) == 0 {
		return TrackState{}, fmt.Errorf("track %s has no common codec", id)
	}
	if minBitrate <= 0 || minBitrate > maxBitrate {
		return TrackState{}, fmt.Errorf("track %s has no common bitrate", id)
	}
	state := TrackState{
		ID:         id,
		SourceID:   track.SourceID,
		Kind:       track.Kind,
		Owner:      track.Owner,
		Direction:  directions[len(directions)-1],
		Codec:      codecs[len(codecs)-1],
		BitrateBps: maxBitrate,
		Revision:   revision,
	}
	if track.Kind == "audio" {
		sampleRates := commonInts(offeredCap.SampleRates, track.SampleRates, answeredCap.SampleRates, answered.SampleRates)
		if len(sampleRates) == 0 {
			return TrackState{}, fmt.Errorf("track %s has no common sample rate", id)
		}
		channelsList := commonInts(offeredCap.Channels, track.Channels, answeredCap.Channels, answered.Channels)
		if len(channelsList) == 0 {
			return TrackState{}, fmt.Errorf("track %s has no common channel count", id)
		}
		state.SampleRate = sampleRates[len(sampleRates)-1]
		state.Channels = channelsList[len(channelsList)-1]
	}
	if track.Kind == "video" {
		resolutions := commonSizes(offeredCap.Resolutions, track.Resolutions, answeredCap.Resolutions, answered.Resolutions)
		if len(resolutions) == 0 {
			return TrackState{}, fmt.Errorf("track %s has no common resolution", id)
		}
		state.Resolution = resolutions[len(resolutions)-1]
	}
	return state, nil
}

// negotiatePairSet validates and negotiates a complete offer/answer pair.
// Every offered track must appear in the answer. Disabled tracks on either
// side are dropped; parameter failures make the whole exchange invalid so the
// previous session stays intact (legacy two-party semantics).
func negotiatePairSet(peers map[string]Peer, ownerID, responderID string, offer, answer []TrackProposal, revision int) ([]TrackState, error) {
	owner, ok := peers[ownerID]
	if !ok {
		return nil, fmt.Errorf("unknown participant %s", ownerID)
	}
	responder, ok := peers[responderID]
	if !ok {
		return nil, fmt.Errorf("unknown participant %s", responderID)
	}
	offers := proposalIndex(offer)
	answers := proposalIndex(answer)
	if len(offers) != len(offer) || len(answers) != len(answer) {
		return nil, fmt.Errorf("duplicate track id in proposal")
	}
	ids := make([]string, 0, len(offers))
	for id, offered := range offers {
		answered, ok := answers[id]
		if !ok {
			return nil, fmt.Errorf("track %s missing from answer", id)
		}
		if offered.Kind != answered.Kind {
			return nil, fmt.Errorf("track %s kind mismatch", id)
		}
		if _, ok := owner.Capabilities[id]; !ok {
			return nil, fmt.Errorf("track %s is unsupported by %s", id, ownerID)
		}
		if _, ok := responder.Capabilities[id]; !ok {
			return nil, fmt.Errorf("track %s is unsupported by %s", id, responderID)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]TrackState, 0, len(ids))
	for _, id := range ids {
		offered := offers[id]
		answered := answers[id]
		offered.Owner = ownerID
		if len(offered.Subscribers) == 0 {
			offered.Subscribers = []string{responderID}
		}
		if !offered.Enabled || !answered.Enabled {
			continue
		}
		state, err := negotiatePair(offered, owner, responder, answered, revision)
		if err != nil {
			return nil, err
		}
		state.Subscribers = append([]string(nil), offered.Subscribers...)
		result = append(result, state)
	}
	return result, nil
}

func maxInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}

func minInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}
