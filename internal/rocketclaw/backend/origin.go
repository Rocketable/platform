package backend

import (
	"slices"
	"strings"
	"time"
)

type originKind string

const (
	originCron        originKind = "cron"
	originExternalMCP originKind = "external_mcp"
)

type cronRunKind string

const (
	cronScheduled cronRunKind = "scheduled"
	cronOneOff    cronRunKind = "one-off"
)

type originPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// cronOrigin is the chat origin of a conversation a cron run created.
type cronOrigin struct {
	Agent      string      `json:"agent"`
	Kind       originKind  `json:"kind"`
	RanAt      string      `json:"ranAt"`
	RunID      string      `json:"runId"`
	RunKind    cronRunKind `json:"runKind"`
	SourcePath string      `json:"sourcePath"`
	Stem       string      `json:"stem"`
}

type externalMCPOrigin struct {
	Agent                  string       `json:"agent"`
	ExternalConversationID string       `json:"externalConversationId"`
	Kind                   originKind   `json:"kind"`
	Pairs                  []originPair `json:"pairs"`
}

// DecideOrigin applies the origin rules to stored facts and returns the origin with the
// lowercased text origin search matches; a nil origin means none.
func DecideOrigin(facts *ChatOriginFacts) (origin any, text string) {
	mcpOn := facts.Binding.ManagedConversationID == facts.ConversationID
	if mcpOn == (facts.Run != nil) {
		return nil, ""
	}

	if mcpOn {
		pairs := make([]originPair, 0, len(facts.Binding.OriginPairs))
		texts := make([]string, 0, len(facts.Binding.OriginPairs))

		for key, value := range facts.Binding.OriginPairs {
			pairs = append(pairs, originPair{Key: key, Value: value})
		}

		slices.SortFunc(pairs, func(a, b originPair) int {
			return strings.Compare(a.Key, b.Key)
		})

		for _, pair := range pairs {
			texts = append(texts, pair.Key+"="+pair.Value)
		}

		return externalMCPOrigin{Kind: originExternalMCP, ExternalConversationID: facts.ExternalConversationID, Agent: facts.Binding.Agent, Pairs: pairs},
			strings.ToLower("External MCP External conversation: " + facts.ExternalConversationID + " Agent: " + facts.Binding.Agent + " " + strings.Join(texts, " "))
	}

	kind := cronScheduled
	if facts.Run.OneOff {
		kind = cronOneOff
	}

	cron := cronOrigin{Kind: originCron, SourcePath: facts.Run.Path, Stem: strings.TrimSuffix(strings.TrimPrefix(facts.Run.Path, "cron/"), ".md"), RunKind: kind, RunID: facts.CreatingSource, Agent: facts.ProducerAgent, RanAt: facts.Run.At.Format(time.RFC3339Nano)}

	return cron, strings.ToLower("Cron Source: " + cron.SourcePath + " Stem: " + cron.Stem + " Run kind: " + string(cron.RunKind) + " Run ID: " + cron.RunID + " Agent: " + cron.Agent + " Ran at: " + cron.RanAt)
}
