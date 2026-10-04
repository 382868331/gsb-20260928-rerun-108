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

func negotiateTrack(track TrackProposal, owner Peer, answers map[string]TrackProposal, answerers map[string]Peer, revision int) (TrackState, error) {
	offerer := owner
	proposal := track
	id := track.ID
	groups := [][]string{
		offerer.Capabilities[id].Directions,
		[]string{proposal.Direction},
	}
	codecGroups := [][]string{
		offerer.Capabilities[id].Codecs,
		proposal.Codecs,
	}
	minBitrate := maxInt(proposal.BitrateMin, offerer.Capabilities[id].BitrateMin)
	maxBitrate := minInt(proposal.BitrateMax, offerer.Capabilities[id].BitrateMax)
	sampleGroups := [][]int{offerer.Capabilities[id].SampleRates, proposal.SampleRates}
	channelGroups := [][]int{offerer.Capabilities[id].Channels, proposal.Channels}
	sizeGroups := [][]Size{offerer.Capabilities[id].Resolutions, proposal.Resolutions}
	for responder, answered := range answers {
		answerer := answerers[responder]
		groups = append(groups, answerer.Capabilities[id].Directions, []string{answered.Direction})
		codecGroups = append(codecGroups, answerer.Capabilities[id].Codecs, answered.Codecs)
		minBitrate = maxInt(minBitrate, answered.BitrateMin, answerer.Capabilities[id].BitrateMin)
		maxBitrate = minInt(maxBitrate, answered.BitrateMax, answerer.Capabilities[id].BitrateMax)
		sampleGroups = append(sampleGroups, answerer.Capabilities[id].SampleRates, answered.SampleRates)
		channelGroups = append(channelGroups, answerer.Capabilities[id].Channels, answered.Channels)
		sizeGroups = append(sizeGroups, answerer.Capabilities[id].Resolutions, answered.Resolutions)
	}
	directions := commonStrings(groups...)
	if len(directions) == 0 {
		return TrackState{}, fmt.Errorf("track %s has no common direction", id)
	}
	codecs := commonStrings(codecGroups...)
	if len(codecs) == 0 {
		return TrackState{}, fmt.Errorf("track %s has no common codec", id)
	}
	if minBitrate <= 0 || minBitrate > maxBitrate {
		return TrackState{}, fmt.Errorf("track %s has no common bitrate", id)
	}
	state := TrackState{
		ID:         id,
		Kind:       proposal.Kind,
		Owner:      proposal.Owner,
		Direction:  directions[len(directions)-1],
		Codec:      codecs[len(codecs)-1],
		BitrateBps: maxBitrate,
		Revision:   revision,
	}
	if proposal.Kind == "audio" {
		sampleRates := commonInts(sampleGroups...)
		if len(sampleRates) == 0 {
			return TrackState{}, fmt.Errorf("track %s has no common sample rate", id)
		}
		channelsList := commonInts(channelGroups...)
		if len(channelsList) == 0 {
			return TrackState{}, fmt.Errorf("track %s has no common channel count", id)
		}
		state.SampleRate = sampleRates[len(sampleRates)-1]
		state.Channels = channelsList[len(channelsList)-1]
	}
	if proposal.Kind == "video" {
		resolutions := commonSizes(sizeGroups...)
		if len(resolutions) == 0 {
			return TrackState{}, fmt.Errorf("track %s has no common resolution", id)
		}
		state.Resolution = resolutions[len(resolutions)-1]
	}
	return state, nil
}

func negotiateTracks(peers map[string]Peer, initiator, responder string, offer, answer []TrackProposal) ([]TrackState, error) {
	offerer, ok := peers[initiator]
	if !ok {
		return nil, fmt.Errorf("unknown peer %s", initiator)
	}
	answerer, ok := peers[responder]
	if !ok {
		return nil, fmt.Errorf("unknown peer %s", responder)
	}
	offers := proposalIndex(offer)
	answers := proposalIndex(answer)
	if len(offers) != len(offer) || len(answers) != len(answer) {
		return nil, fmt.Errorf("duplicate track id in proposal")
	}
	if len(offers) != len(answers) {
		return nil, fmt.Errorf("offer and answer must describe the same track set")
	}

	ids := make([]string, 0, len(offers))
	for id := range offers {
		if _, ok := answers[id]; !ok {
			return nil, fmt.Errorf("track %s missing from answer", id)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	result := make([]TrackState, 0, len(ids))
	for _, id := range ids {
		offered := offers[id]
		answered := answers[id]
		if offered.Kind != answered.Kind {
			return nil, fmt.Errorf("track %s kind mismatch", id)
		}
		offeredCap, ok := offerer.Capabilities[id]
		if !ok {
			return nil, fmt.Errorf("track %s is unsupported by %s", id, initiator)
		}
		answeredCap, ok := answerer.Capabilities[id]
		if !ok {
			return nil, fmt.Errorf("track %s is unsupported by %s", id, responder)
		}
		if offeredCap.Kind != offered.Kind || answeredCap.Kind != offered.Kind {
			return nil, fmt.Errorf("track %s kind is outside capability metadata", id)
		}
		offered.Owner = initiator
		if len(offered.Subscribers) == 0 {
			offered.Subscribers = []string{responder}
		}
		if !offered.Enabled || !answered.Enabled {
			continue
		}
		state, err := negotiateTrack(offered, offerer, map[string]TrackProposal{responder: answered}, map[string]Peer{responder: answerer}, 0)
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
