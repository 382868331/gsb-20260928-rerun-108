package main

import "fmt"

func (r *RoomService) newAdjustmentLocked(room *Room, initiatorPID string, items []AdjustItem, complete bool) *Adjustment {
	adjust := &Adjustment{
		ID:           room.NextAdjustment,
		BasedOn:      room.Revision,
		InitiatorPID: initiatorPID,
		Items:        items,
		Status:       AdjustPending,
	}
	room.NextAdjustment++
	room.Adjustments[adjust.ID] = adjust
	if complete {
		recomputeAdjustStatus(adjust)
	}
	return adjust
}

func itemKindToKind(room *Room, item *AdjustItem) string {
	if track := room.trackByGen(item.TrackID, item.TrackGen); track != nil {
		return track.Kind
	}
	return ""
}

// rememberRejectionLocked 追加明确拒绝记录。后续即使同名参与身份重新加入，
// 也必须显式重新订阅，不能把旧拒绝覆盖成接受。
func (r *RoomService) rememberRejectionLocked(room *Room, item *AdjustItem, reason string) {
	subscriberPID := item.TargetPID
	latest := room.activeSub(item.TrackID, item.TrackGen, subscriberPID)
	if latest != nil && latest.Status == SubRejected && latest.Reason == reason {
		return
	}
	participant := room.Participations[subscriberPID]
	name := ""
	generation := 0
	if participant != nil {
		name = participant.Name
		generation = participant.Generation
	}
	room.Subscriptions = append(room.Subscriptions, &Subscription{
		TrackID:         item.TrackID,
		OwnerGeneration: item.TrackGen,
		Kind:            itemKindToKind(room, item),
		SubscriberPID:   subscriberPID,
		SubscriberName:  name,
		SubscriberGen:   generation,
		Status:          SubRejected,
		Reason:          reason,
		Revision:        room.Revision,
	})
}

// applyAcceptedLocked 追加一条新生效订阅记录。它按完整参与身份落库，因此
// 两个不同参与身份给出的同意不会被拼成同一个接受者。
func (r *RoomService) applyAcceptedLocked(room *Room, item *AdjustItem, decider, subscriber *Participation, track *RTrack, params MediaParams) {
	room.Subscriptions = append(room.Subscriptions, &Subscription{
		TrackID:         track.ID,
		OwnerGeneration: track.OwnerGeneration,
		Kind:            track.Kind,
		SubscriberPID:   subscriber.ID,
		SubscriberName:  subscriber.Name,
		SubscriberGen:   subscriber.Generation,
		Status:          SubActive,
		Params:          params,
		Revision:        room.Revision,
	})
}

func validateRoomParams(caps map[string]TrackCapability, id, kind string, params MediaParams) error {
	capability, ok := caps[id]
	if !ok {
		return fmt.Errorf("track %s is outside capability set", id)
	}
	if capability.Kind != kind {
		return fmt.Errorf("track %s kind mismatch", id)
	}
	if params.Direction != "" && !containsString(capability.Directions, params.Direction) {
		return fmt.Errorf("direction %s unsupported for %s", params.Direction, id)
	}
	if params.Codec != "" && !containsString(capability.Codecs, params.Codec) {
		return fmt.Errorf("codec %s unsupported for %s", params.Codec, id)
	}
	if params.BitrateBps != 0 && (params.BitrateBps < capability.BitrateMin || params.BitrateBps > capability.BitrateMax) {
		return fmt.Errorf("bitrate %d outside %s range", params.BitrateBps, id)
	}
	if params.SampleRate != 0 && !containsInt(capability.SampleRates, params.SampleRate) {
		return fmt.Errorf("sample rate %d unsupported for %s", params.SampleRate, id)
	}
	if params.Channels != 0 && !containsInt(capability.Channels, params.Channels) {
		return fmt.Errorf("channels %d unsupported for %s", params.Channels, id)
	}
	if params.Width != 0 || params.Height != 0 {
		if !containsSize(capability.Resolutions, Size{Width: params.Width, Height: params.Height}) {
			return fmt.Errorf("resolution %dx%d unsupported for %s", params.Width, params.Height, id)
		}
	}
	return nil
}

// resolveRoomParams 求交发布能力、接收能力、发起方请求与答复方响应；
// update 以当前生效参数为基线，清楚区分各自基于的状态。
func resolveRoomParams(track *RTrack, publisherCaps, subscriberCaps map[string]TrackCapability, item *AdjustItem, answer MediaParams) (MediaParams, error) {
	id := track.ID
	pub, ok := publisherCaps[id]
	if !ok {
		return MediaParams{}, fmt.Errorf("track %s unsupported by publisher", id)
	}
	sub, ok := subscriberCaps[id]
	if !ok {
		return MediaParams{}, fmt.Errorf("track %s unsupported by subscriber", id)
	}
	base := track.Params
	if item.Kind == ItemUpdate {
		base = mergeParams(track.Params, item.Active)
	}
	requested := mergeParams(base, item.Offer)
	resolved := mergeParams(requested, answer)
	if resolved.Direction == "" {
		resolved.Direction = pub.Directions[0]
	}
	directions := commonStrings(pub.Directions, sub.Directions, []string{resolved.Direction})
	if len(directions) == 0 {
		return MediaParams{}, fmt.Errorf("track %s has no common direction", id)
	}
	resolved.Direction = directions[len(directions)-1]
	if resolved.Codec == "" {
		resolved.Codec = pub.Codecs[0]
	}
	codecs := commonStrings(pub.Codecs, sub.Codecs, []string{resolved.Codec})
	if len(codecs) == 0 {
		return MediaParams{}, fmt.Errorf("track %s has no common codec", id)
	}
	resolved.Codec = codecs[len(codecs)-1]
	minBitrate := maxInt(pub.BitrateMin, sub.BitrateMin)
	maxBitrate := minInt(pub.BitrateMax, sub.BitrateMax)
	if resolved.BitrateBps == 0 {
		resolved.BitrateBps = maxBitrate
	}
	if resolved.BitrateBps < minBitrate || resolved.BitrateBps > maxBitrate {
		return MediaParams{}, fmt.Errorf("track %s bitrate %d outside common range", id, resolved.BitrateBps)
	}
	if track.Kind == "audio" {
		sampleRates := commonInts(pub.SampleRates, sub.SampleRates)
		if len(sampleRates) == 0 {
			return MediaParams{}, fmt.Errorf("track %s has no common sample rate", id)
		}
		if resolved.SampleRate == 0 {
			resolved.SampleRate = sampleRates[len(sampleRates)-1]
		} else if !containsInt(sampleRates, resolved.SampleRate) {
			return MediaParams{}, fmt.Errorf("track %s sample rate %d not common", id, resolved.SampleRate)
		}
		channels := commonInts(pub.Channels, sub.Channels)
		if len(channels) == 0 {
			return MediaParams{}, fmt.Errorf("track %s has no common channels", id)
		}
		if resolved.Channels == 0 {
			resolved.Channels = channels[len(channels)-1]
		} else if !containsInt(channels, resolved.Channels) {
			return MediaParams{}, fmt.Errorf("track %s channels %d not common", id, resolved.Channels)
		}
	}
	if track.Kind == "video" {
		sizes := commonSizes(pub.Resolutions, sub.Resolutions)
		if len(sizes) == 0 {
			return MediaParams{}, fmt.Errorf("track %s has no common resolution", id)
		}
		if resolved.Width == 0 && resolved.Height == 0 {
			chosen := sizes[len(sizes)-1]
			resolved.Width, resolved.Height = chosen.Width, chosen.Height
		} else if !containsSize(sizes, Size{Width: resolved.Width, Height: resolved.Height}) {
			return MediaParams{}, fmt.Errorf("track %s resolution %dx%d not common", id, resolved.Width, resolved.Height)
		}
	}
	return resolved, nil
}

func mergeParams(base, override MediaParams) MediaParams {
	out := base
	if override.Direction != "" {
		out.Direction = override.Direction
	}
	if override.Codec != "" {
		out.Codec = override.Codec
	}
	if override.BitrateBps != 0 {
		out.BitrateBps = override.BitrateBps
	}
	if override.SampleRate != 0 {
		out.SampleRate = override.SampleRate
	}
	if override.Channels != 0 {
		out.Channels = override.Channels
	}
	if override.Width != 0 {
		out.Width = override.Width
	}
	if override.Height != 0 {
		out.Height = override.Height
	}
	return out
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func containsInt(items []int, value int) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func containsSize(items []Size, value Size) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func cloneMediaParams(params MediaParams) MediaParams { return params }

func cloneAdjustment(adjust *Adjustment) *Adjustment {
	if adjust == nil {
		return nil
	}
	copy := *adjust
	copy.Items = make([]AdjustItem, len(adjust.Items))
	for i, item := range adjust.Items {
		c := item
		c.Decisions = map[string]ItemStatus{}
		for key, value := range item.Decisions {
			c.Decisions[key] = value
		}
		c.Rejections = map[string]string{}
		for key, value := range item.Rejections {
			c.Rejections[key] = value
		}
		copy.Items[i] = c
	}
	return &copy
}

func cloneRoom(room *Room) *Room {
	if room == nil {
		return nil
	}
	copy := *room
	copy.NextGeneration = map[string]int{}
	for key, value := range room.NextGeneration {
		copy.NextGeneration[key] = value
	}
	copy.Participations = map[string]*Participation{}
	for key, value := range room.Participations {
		p := *value
		copy.Participations[key] = &p
	}
	copy.PIDOrder = append([]string(nil), room.PIDOrder...)
	copy.TrackHistory = map[string][]*RTrack{}
	for key, value := range room.TrackHistory {
		copied := make([]*RTrack, len(value))
		for i, track := range value {
			t := *track
			copied[i] = &t
		}
		copy.TrackHistory[key] = copied
	}
	copy.Subscriptions = make([]*Subscription, len(room.Subscriptions))
	for i, sub := range room.Subscriptions {
		s := *sub
		copy.Subscriptions[i] = &s
	}
	copy.Adjustments = map[int]*Adjustment{}
	for key, value := range room.Adjustments {
		copy.Adjustments[key] = cloneAdjustment(value)
	}
	return &copy
}
