package harnessfeedback

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/slack-go/slack"
	_ "modernc.org/sqlite"
)

const (
	issueWindow = 14 * 24 * time.Hour
	slackLimit  = 3000
	reportLimit = 4000
	matchPrompt = `You group the reports agents file about problems with their own harness: its tools, permissions, sandbox, errors and descriptions.

You will receive a numbered list of known issues and a new report. Reply with the number of the known issue the report describes, even in other words, or 0 when it describes a different problem. Reply with the number only.`
	toolDescription = "Call this whenever your own harness gets in your way and you have to work around it: a confusing or wrong tool result or error, a misleading tool description, a missing capability, or a permission or sandbox surprise. Report each problem once, then carry on with your task. Not for problems in the task itself, the user's code or outside services. People read these reports to fix the harness. Leave out secrets, credentials, and customer or other personal data (PII): describe the harness behavior, not the data you were handling."
)

type issue struct {
	id                   int64
	summary, channel, ts string
}

type reportInput struct {
	Summary string `json:"summary" jsonschema:"one line naming the problem"`
	Details string `json:"details" jsonschema:"what you tried, what happened, what you expected and how you worked around it, with exact tool names and error text; remove secrets and personal data from it"`
}

type reporter struct {
	slack     *slack.Client
	model     openai.Client
	modelName string
	channel   string
	db        *sql.DB
	session   string
}

func Run(ctx context.Context, getenv func(string) string) error {
	var missing []string

	value := func(key string) string {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			missing = append(missing, key)
		}

		return v
	}

	path := value("HARNESS_FEEDBACK_DB")

	r := reporter{
		slack:     slack.New(value("HARNESS_FEEDBACK_SLACK_TOKEN")),
		channel:   value("HARNESS_FEEDBACK_SLACK_CHANNEL"),
		model:     openai.NewClient(option.WithBaseURL(value("HARNESS_FEEDBACK_MODEL_URL")), option.WithAPIKey(value("HARNESS_FEEDBACK_MODEL_KEY")), option.WithUnsafeAllowHTTP()),
		modelName: value("HARNESS_FEEDBACK_MODEL"),
		session:   value("HARNESS_FEEDBACK_WEB_ORIGIN") + "/s/" + base64.RawURLEncoding.EncodeToString([]byte(value("ROCKETCLAW_CONVERSATION_ID"))),
	}

	if len(missing) > 0 {
		return fmt.Errorf("harness feedback needs %s", strings.Join(missing, ", "))
	}

	var err error
	if r.db, err = openIssues(ctx, path); err != nil {
		return err
	}
	defer func() { _ = r.db.Close() }()

	if err := r.server().Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("serve harness feedback: %w", err)
	}

	return nil
}

func (r *reporter) server() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "harness-feedback", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "report", Description: toolDescription}, r.report)

	return server
}

func (r *reporter) report(ctx context.Context, _ *mcp.CallToolRequest, in reportInput) (*mcp.CallToolResult, any, error) {
	summary := strings.Join(strings.Fields(in.Summary), " ")
	if summary == "" {
		return nil, nil, errors.New("report needs a summary")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("lock issues: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE reported_at < ?`, now-int64(issueWindow/time.Second)); err != nil {
		return nil, nil, fmt.Errorf("drop old issues: %w", err)
	}

	issues, err := recentIssues(ctx, tx)
	if err != nil {
		return nil, nil, err
	}

	match, err := r.match(ctx, summary+"\n\n"+in.Details, issues)
	if err != nil {
		return nil, nil, err
	}

	text, channel, thread, result := "New harness issue: "+summary, r.channel, "", "Reported as a new harness issue."

	if match > 0 {
		known := issues[match-1]
		text, channel, thread, result = "Reported again: "+summary, known.channel, known.ts, "Added to the known harness issue: "+known.summary+"."
	}

	message := []rune(text + "\nSession " + r.session + "\n\n" + in.Details)
	text = string(message[:min(len(message), slackLimit)])
	options := []slack.MsgOption{slack.MsgOptionText(text, false), slack.MsgOptionBlocks(slack.NewSectionBlock(slack.NewTextBlockObject(slack.PlainTextType, text, false, false), nil, nil)), slack.MsgOptionDisableMarkdown(), slack.MsgOptionParse(false)}

	if thread != "" {
		options = append(options, slack.MsgOptionTS(thread))
	}

	channelID, ts, err := r.slack.PostMessageContext(ctx, channel, options...)
	if err != nil {
		return nil, nil, fmt.Errorf("post to Slack: %w", err)
	}

	save, args := `INSERT INTO issues (summary, channel, ts, reported_at) VALUES (?, ?, ?, ?)`, []any{summary, channelID, ts, now}
	if match > 0 {
		save, args = `UPDATE issues SET reported_at = ? WHERE id = ?`, []any{now, issues[match-1].id}
	}

	if _, err := tx.ExecContext(ctx, save, args...); err != nil {
		return nil, nil, fmt.Errorf("save issue: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit issue: %w", err)
	}

	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: result}}}, nil, nil
}

func (r *reporter) match(ctx context.Context, report string, issues []issue) (int, error) {
	if len(issues) == 0 {
		return 0, nil
	}

	var input strings.Builder

	input.WriteString("Known issues:\n")

	for i, known := range issues {
		fmt.Fprintf(&input, "%d. %s\n", i+1, known.summary)
	}

	input.WriteString("\nNew report:\n<report>\n" + strings.ToValidUTF8(report[:min(len(report), reportLimit)], "") + "\n</report>\n\nReply with only the number.")

	completion, err := r.model.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{Model: r.modelName, Messages: []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(matchPrompt), openai.UserMessage(input.String())}})
	if err != nil {
		return 0, fmt.Errorf("ask the model: %w", err)
	}

	if len(completion.Choices) == 0 {
		return 0, errors.New("the model returned no answer")
	}

	answer := completion.Choices[0].Message.Content

	match, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || match < 0 || match > len(issues) {
		return 0, fmt.Errorf("the model answered %q, not an issue number", answer)
	}

	return match, nil
}

func openIssues(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(300000)")
	if err != nil {
		return nil, fmt.Errorf("open issues database: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS issues (id INTEGER PRIMARY KEY, summary TEXT NOT NULL, channel TEXT NOT NULL, ts TEXT NOT NULL, reported_at INTEGER NOT NULL)`); err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("create issues table: %w", err)
	}

	return db, nil
}

func recentIssues(ctx context.Context, tx *sql.Tx) ([]issue, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, summary, channel, ts FROM issues ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var issues []issue

	for rows.Next() {
		var known issue
		if err := rows.Scan(&known.id, &known.summary, &known.channel, &known.ts); err != nil {
			return nil, fmt.Errorf("read issue: %w", err)
		}

		issues = append(issues, known)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read issues: %w", err)
	}

	return issues, nil
}
