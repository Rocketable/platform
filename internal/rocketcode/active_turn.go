package rocketcode

import (
	"slices"
)

// ReplayAttribution overrides a recovered half-open replay range, including unknown values.
type ReplayAttribution struct {
	Start           int     `json:"start"`
	End             int     `json:"end"`
	Agent           string  `json:"agent,omitempty"`
	Model           string  `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

// RemapReplayAttribution moves overrides to projected replay boundaries in place.
func RemapReplayAttribution(ranges []ReplayAttribution, boundaries []int) {
	for i := range ranges {
		ranges[i].Start = boundaries[ranges[i].Start]
		ranges[i].End = boundaries[ranges[i].End]
	}
}

// AttributionAt returns the execution that produced a replay item.
func (e *SessionEntry) AttributionAt(index int) ReplayAttribution {
	for _, attribution := range e.ReplayAttribution {
		if index >= attribution.Start && index < attribution.End {
			return attribution
		}
	}

	return ReplayAttribution{Agent: e.Agent, Model: e.Model, ReasoningEffort: e.ReasoningEffort}
}

func (e *SessionEntry) attributionRanges(offset int) []ReplayAttribution {
	ranges := slices.Clone(e.ReplayAttribution)

	ranges = append(ranges, ReplayAttribution{End: len(e.ReplayInput), Agent: e.Agent, Model: e.Model, ReasoningEffort: e.ReasoningEffort})
	for i := range ranges {
		ranges[i].Start += offset
		ranges[i].End += offset
	}

	return ranges
}
