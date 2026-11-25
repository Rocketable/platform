package backend

import (
	"slices"
	"strings"
	"time"
)

type originKind string

const (
	originRoutine     originKind = "routine"
	originExternalMCP originKind = "external_mcp"
)

type routineRunKind string

const (
	routineScheduled routineRunKind = "scheduled"
	routineOneOff    routineRunKind = "one-off"
)

type originPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type routineOrigin struct {
	Agent      string         `json:"agent"`
	Kind       originKind     `json:"kind"`
	RanAt      string         `json:"ranAt"`
	RunID      string         `json:"runId"`
	RunKind    routineRunKind `json:"runKind"`
	SourcePath string         `json:"sourcePath"`
	Stem       string         `json:"stem"`
}

type externalMCPOrigin struct {
	Agent                  string       `json:"agent"`
	ExternalConversationID string       `json:"externalConversationId"`
	Kind                   originKind   `json:"kind"`
	Pairs                  []originPair `json:"pairs"`
}

func DecideOrigin(facts *ChatOriginFacts) (origin any, text string) {
	mcpOn := facts.Binding.ManagedConversationID == facts.ConversationID
	if mcpOn == (facts.Run != nil) {
		return nil, ""
	}

	if mcpOn {
		pairs := make([]originPair, 0, len(facts.Binding.OriginPairs))
		size := max(0, len(facts.Binding.OriginPairs)-1)

		for key, value := range facts.Binding.OriginPairs {
			pairs = append(pairs, originPair{Key: key, Value: value})
			size += len(key) + len(value) + 1
		}

		slices.SortFunc(pairs, func(a, b originPair) int {
			return strings.Compare(a.Key, b.Key)
		})

		var joined strings.Builder
		joined.Grow(size)

		for i, pair := range pairs {
			if i > 0 {
				joined.WriteByte(' ')
			}

			joined.WriteString(pair.Key)
			joined.WriteByte('=')
			joined.WriteString(pair.Value)
		}

		return externalMCPOrigin{Kind: originExternalMCP, ExternalConversationID: facts.ExternalConversationID, Agent: facts.Binding.Agent, Pairs: pairs},
			strings.ToLower("External MCP External conversation: " + facts.ExternalConversationID + " Agent: " + facts.Binding.Agent + " " + joined.String())
	}

	kind := routineScheduled
	if facts.Run.OneOff {
		kind = routineOneOff
	}

	routine := routineOrigin{Kind: originRoutine, SourcePath: facts.Run.Path, Stem: RoutineStem(facts.Run.Path), RunKind: kind, RunID: facts.CreatingSource, Agent: facts.ProducerAgent, RanAt: facts.Run.At.Format(time.RFC3339Nano)}

	return routine, strings.ToLower("Routine Source: " + routine.SourcePath + " Stem: " + routine.Stem + " Run kind: " + string(routine.RunKind) + " Run ID: " + routine.RunID + " Agent: " + routine.Agent + " Ran at: " + routine.RanAt)
}
