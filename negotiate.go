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

func negotiateTracks(peers map[string]Peer, initiator, responder string, offer, answer []TrackProposal) ([]TrackState, error) {
	offerer, ok := peers[initiator]
	if !ok {
		return nil, fmt.Errorf("unknown peer %s", initiator)
	}
	answerer, ok := peers[responder]
	if !ok {
		return nil, fmt.Errorf("unknown peer %s", responder)
	}
	var _ = capabilityIndex
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
		if !offered.Enabled || !answered.Enabled {
			continue
		}

		directions := commonStrings(offeredCap.Directions, answeredCap.Directions, []string{offered.Direction}, []string{answered.Direction})
		if len(directions) == 0 {
			return nil, fmt.Errorf("track %s has no common direction", id)
		}
		codecs := commonStrings(offeredCap.Codecs, answeredCap.Codecs, offered.Codecs, answered.Codecs)
		if len(codecs) == 0 {
			return nil, fmt.Errorf("track %s has no common codec", id)
		}
		minBitrate := maxInt(offered.BitrateMin, answered.BitrateMin, offeredCap.BitrateMin, answeredCap.BitrateMin)
		maxBitrate := minInt(offered.BitrateMax, answered.BitrateMax, offeredCap.BitrateMax, answeredCap.BitrateMax)
		if minBitrate <= 0 || minBitrate > maxBitrate {
			return nil, fmt.Errorf("track %s has no common bitrate", id)
		}

		state := TrackState{
			ID:         id,
			Kind:       offered.Kind,
			Direction:  directions[len(directions)-1],
			Codec:      codecs[len(codecs)-1],
			BitrateBps: maxBitrate,
		}
		if offered.Kind == "audio" {
			sampleRates := commonInts(offeredCap.SampleRates, answeredCap.SampleRates, offered.SampleRates, answered.SampleRates)
			if len(sampleRates) == 0 {
				return nil, fmt.Errorf("track %s has no common sample rate", id)
			}
			channelsList := commonInts(offeredCap.Channels, answeredCap.Channels, offered.Channels, answered.Channels)
			if len(channelsList) == 0 {
				return nil, fmt.Errorf("track %s has no common channel count", id)
			}
			state.SampleRate = sampleRates[len(sampleRates)-1]
			state.Channels = channelsList[len(channelsList)-1]
		}
		if offered.Kind == "video" {
			resolutions := commonSizes(offeredCap.Resolutions, answeredCap.Resolutions, offered.Resolutions, answered.Resolutions)
			if len(resolutions) == 0 {
				return nil, fmt.Errorf("track %s has no common resolution", id)
			}
			state.Resolution = resolutions[len(resolutions)-1]
		}
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
