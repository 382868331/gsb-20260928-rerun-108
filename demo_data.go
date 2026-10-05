package main

func demoCapabilities() map[string]TrackCapability {
	return map[string]TrackCapability{
		"audio-1": {
			ID:          "audio-1",
			Kind:        "audio",
			Directions:  []string{string(DirectionSendRecv), string(DirectionInactive)},
			Codecs:      []string{"opus"},
			BitrateMin:  16_000,
			BitrateMax:  96_000,
			SampleRates: []int{48_000},
			Channels:    []int{1, 2},
		},
		"video-1": {
			ID:          "video-1",
			Kind:        "video",
			Directions:  []string{string(DirectionSendRecv), string(DirectionInactive)},
			Codecs:      []string{"VP8", "H264"},
			BitrateMin:  100_000,
			BitrateMax:  1_000_000,
			Resolutions: []Size{{Width: 640, Height: 480}, {Width: 1280, Height: 720}},
		},
	}
}

func fullProposal(videoBitrate int) []TrackProposal {
	return []TrackProposal{
		{
			ID:          "audio-1",
			Kind:        "audio",
			Enabled:     true,
			Direction:   string(DirectionSendRecv),
			Codecs:      []string{"opus"},
			BitrateMin:  32_000,
			BitrateMax:  64_000,
			SampleRates: []int{48_000},
			Channels:    []int{2},
		},
		{
			ID:          "video-1",
			Kind:        "video",
			Enabled:     true,
			Direction:   string(DirectionSendRecv),
			Codecs:      []string{"VP8", "H264"},
			BitrateMin:  videoBitrate,
			BitrateMax:  videoBitrate,
			Resolutions: []Size{{Width: 1280, Height: 720}},
		},
	}
}

func audioOnlyProposal() []TrackProposal {
	proposal := fullProposal(500_000)
	proposal[1].Enabled = false
	return proposal
}

func invalidCodecProposal() []TrackProposal {
	proposal := audioOnlyProposal()
	proposal[0].Codecs = []string{"G722"}
	return proposal
}
