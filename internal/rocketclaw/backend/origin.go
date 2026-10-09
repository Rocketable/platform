package backend

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
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

// CronRun is a cron run parsed from its source conversation ID.
type CronRun struct {
	kind cronRunKind
	path string
	Stem string
	At   time.Time
}

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
	locator, cronOn := creatingCronLocator(facts.ConversationID, facts.CreatedBy, facts.CreatingSource)

	mcpOn := facts.Binding.ManagedConversationID == facts.ConversationID
	if mcpOn == cronOn {
		return nil, ""
	}

	if mcpOn {
		pairs := make([]originPair, 0, len(facts.Binding.OriginPairs))
		texts := make([]string, 0, len(facts.Binding.OriginPairs))

		for _, key := range slices.Sorted(maps.Keys(facts.Binding.OriginPairs)) {
			pairs = append(pairs, originPair{Key: key, Value: facts.Binding.OriginPairs[key]})
			texts = append(texts, key+"="+facts.Binding.OriginPairs[key])
		}

		return externalMCPOrigin{Kind: originExternalMCP, ExternalConversationID: facts.ExternalConversationID, Agent: facts.Binding.Agent, Pairs: pairs},
			strings.ToLower(fmt.Sprintf("External MCP External conversation: %s Agent: %s %s", facts.ExternalConversationID, facts.Binding.Agent, strings.Join(texts, " ")))
	}

	run, _ := ParseCronRun(locator)
	cron := cronOrigin{Kind: originCron, SourcePath: run.path, Stem: run.Stem, RunKind: run.kind, RunID: locator, Agent: facts.ProducerAgent, RanAt: run.At.Format(time.RFC3339Nano)}

	return cron, strings.ToLower(fmt.Sprintf("Cron Source: %s Stem: %s Run kind: %s Run ID: %s Agent: %s Ran at: %s", cron.SourcePath, cron.Stem, cron.RunKind, cron.RunID, cron.Agent, cron.RanAt))
}

func creatingCronLocator(id string, createdBy ThreadCreator, creatingSource string) (string, bool) {
	if source, ok := strings.CutPrefix(id, "web:"); ok {
		if _, parsed := ParseCronRun(source); parsed {
			return source, true
		}
	}

	if _, _, slack := protocol.SlackThreadTarget(id); slack && createdBy != ThreadCreatedByCron {
		return "", false
	}

	if _, ok := ParseCronRun(creatingSource); ok {
		return creatingSource, true
	}

	return "", false
}

// ParseCronRun parses a cron or one-off cron run source conversation ID.
func ParseCronRun(source string) (CronRun, bool) {
	kind, path, ok := cronScheduled, "", false
	if rest, cut := strings.CutPrefix(source, "cron:"); cut {
		path, ok = rest, true
	} else if rest, cut := strings.CutPrefix(source, "one-off-cron:"); cut {
		kind, path, ok = cronOneOff, rest, true
	}

	end := strings.LastIndex(path, ":")

	start := strings.LastIndex(path[:max(end, 0)], ":")
	if !ok || start < 0 {
		return CronRun{}, false
	}

	at, err := time.Parse("20060102T150405.000000000Z", path[start+1:end])
	if err != nil {
		return CronRun{}, false
	}

	relative := path[:start]

	return CronRun{kind: kind, path: relative, Stem: strings.TrimSuffix(strings.TrimPrefix(relative, "cron/"), ".md"), At: at}, true
}
