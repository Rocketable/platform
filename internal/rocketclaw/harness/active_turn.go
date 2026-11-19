package harness

type ReplayAttribution struct {
	Start           int     `json:"start"`
	End             int     `json:"end"`
	Agent           string  `json:"agent,omitempty"`
	Model           string  `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

func RemapReplayAttribution(ranges []ReplayAttribution, boundaries []int) {
	for i := range ranges {
		ranges[i].Start = boundaries[ranges[i].Start]
		ranges[i].End = boundaries[ranges[i].End]
	}
}

func (e *SessionEntry) AttributionAt(index int) ReplayAttribution {
	for _, attribution := range e.ReplayAttribution {
		if index >= attribution.Start && index < attribution.End {
			return attribution
		}
	}

	return ReplayAttribution{Agent: e.Agent, Model: e.Model, ReasoningEffort: e.ReasoningEffort}
}

func (e *SessionEntry) appendAttributionRanges(ranges []ReplayAttribution, offset int) []ReplayAttribution {
	start := len(ranges)
	ranges = append(ranges, e.ReplayAttribution...)

	ranges = append(ranges, ReplayAttribution{End: len(e.ReplayInput), Agent: e.Agent, Model: e.Model, ReasoningEffort: e.ReasoningEffort})
	for i := start; i < len(ranges); i++ {
		ranges[i].Start += offset
		ranges[i].End += offset
	}

	return ranges
}
