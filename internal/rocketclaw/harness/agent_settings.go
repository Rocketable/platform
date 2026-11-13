package harness

import "slices"

// AgentSettingControl is the agent editor's control for a setting.
type AgentSettingControl string

const (
	// AgentSettingText is a free-text field.
	AgentSettingText AgentSettingControl = "text"
	// AgentSettingSwitch is an on/off switch.
	AgentSettingSwitch AgentSettingControl = "switch"
	// AgentSettingChoice picks one of the setting's Choices.
	AgentSettingChoice AgentSettingControl = "choice"
	// AgentSettingList is a list of strings.
	AgentSettingList AgentSettingControl = "list"
	// AgentSettingModel is the model picker.
	AgentSettingModel AgentSettingControl = "model"
	// AgentSettingFile is edited in the agent file itself.
	AgentSettingFile AgentSettingControl = "file"
)

// AgentSetting is the plain-language help for one agent file setting in the modes it lists.
type AgentSetting struct {
	// Key is the setting's path, nested keys joined with dots, such as approver.decision.model.
	Key   string
	Modes []AgentMode
	Group string
	Label string
	// Explanation says what the setting does, for someone who is not an engineer.
	Explanation string
	// Default says in words what leaving the setting out does.
	Default string
	// Allowed says in one sentence which values load.
	Allowed  string
	Control  AgentSettingControl
	Choices  []string
	Required bool
	Common   bool
}

// AgentSettings returns the help for every setting the agent loader reads, in the order an agent file
// lists them: nested settings follow their parent. A setting whose help differs by mode appears once per
// mode.
func AgentSettings() []AgentSetting {
	easy, advanced, both := []AgentMode{AgentModeEasy}, []AgentMode{AgentModeAdvanced}, []AgentMode{AgentModeEasy, AgentModeAdvanced}
	decisionDefault := "The installation's decision model for an easy agent; none for a reviewer agent."

	return []AgentSetting{
		{Key: "mode", Modes: both, Group: "Basics", Label: "Agent type", Control: AgentSettingChoice, Choices: []string{"easy", "advanced"}, Common: true,
			Explanation: "Easy agents turn whole sections on or off; advanced agents spell out every permission rule.",
			Default:     "Advanced.", Allowed: "easy or advanced."},
		{Key: "description", Modes: advanced, Group: "Basics", Label: "Description", Control: AgentSettingText, Common: true,
			Explanation: "A short sentence about what the agent is for, shown when someone picks an agent.",
			Default:     "No description is shown.", Allowed: "Any text."},
		{Key: "space", Modes: easy, Group: "Basics", Label: "Space", Control: AgentSettingText, Required: true,
			Explanation: "The Web space where this agent's chats start and open; it is created when missing.",
			Default:     "None: an easy agent must name a space.", Allowed: "A space name, as one string."},
		{Key: "space", Modes: advanced, Group: "Basics", Label: "Space", Control: AgentSettingText, Common: true,
			Explanation: "The Web space where this agent's chats start and open; it is created when missing.",
			Default:     "Chats start in whatever space they are opened from.", Allowed: "A space name, as one string."},
		{Key: "sticky", Modes: both, Group: "Basics", Label: "Shared chat", Control: AgentSettingChoice, Choices: []string{"off", "user", "global"},
			Explanation: "Whether a new chat starts fresh, reopens the person's own chat (user), or reopens one chat everyone shares (global). A user agent cannot have routines.",
			Default:     "Off: every new chat starts fresh.", Allowed: "off, user or global."},
		{Key: "model", Modes: both, Group: "Model", Label: "Model", Control: AgentSettingModel, Required: true,
			Explanation: "The AI model the agent runs on. Add #low, #medium or #high to set how hard it thinks.",
			Default:     "None: every agent must name a model.", Allowed: "A model name, optionally after a configured provider and a slash, optionally followed by #effort."},
		{Key: "reasoningEffort", Modes: advanced, Group: "Model", Label: "Thinking effort (old)", Control: AgentSettingText,
			Explanation: "An older way to set how hard the model thinks. Prefer the #effort ending on the model, which wins over this.",
			Default:     "The model's own default.", Allowed: "An effort name the model accepts, such as low, medium or high."},
		{Key: "verbosity", Modes: advanced, Group: "Model", Label: "Answer length", Control: AgentSettingText,
			Explanation: "How long and detailed the model's answers are.",
			Default:     "The model's own default.", Allowed: "A verbosity the model accepts, such as low, medium or high."},
		{Key: "subagents", Modes: easy, Group: "Helpers", Label: "Helper models", Control: AgentSettingList,
			Explanation: "Models the agent may run copies of itself on, to hand off part of a task.",
			Default:     "The agent's own model.", Allowed: "auto, or a list of model names, each written like the model setting; [] for none."},
		{Key: "maxRecursion", Modes: easy, Group: "Helpers", Label: "Helper depth", Control: AgentSettingText,
			Explanation: "How many levels deep helpers may hand work to further helpers; 0 turns helpers off.",
			Default:     "1 level.", Allowed: "A whole number, 0 or more."},
		{Key: "maxRecursion", Modes: advanced, Group: "Helpers", Label: "Helper depth", Control: AgentSettingText,
			Explanation: "How many levels deep this agent may hand work to other agents; 0 turns handing off work off.",
			Default:     "No limit.", Allowed: "A whole number of -1 or more; -1 means no limit."},
		{Key: "workingDirectory", Modes: easy, Group: "Folder and shell", Label: "Folder", Control: AgentSettingText, Required: true,
			Explanation: "The agent's own folder, which its shell and file tools may read and change. It is relative to RocketClaw's folder and may use $VARIABLES.",
			Default:     "None: an easy agent must name a folder.", Allowed: "A folder that is not, does not contain, and is not inside the workspace or RocketClaw's own folders."},
		{Key: "shell", Modes: advanced, Group: "Folder and shell", Label: "Shell", Control: AgentSettingText,
			Explanation: "Where the agent's shell commands run: emulated for real Bash in a Linux sandbox, or the full path of a wrapper program.",
			Default:     "Plain bash on the server.", Allowed: "emulated, or the absolute path of one program without arguments."},
		{Key: "workingDirectory", Modes: advanced, Group: "Folder and shell", Label: "Folder", Control: AgentSettingText,
			Explanation: "With the emulated shell, the agent's own folder, which its shell may read and change.",
			Default:     "None: the emulated shell needs a folder; other shells ignore this.", Allowed: "A folder that is not, does not contain, and is not inside the workspace or RocketClaw's own folders."},
		{Key: "commands", Modes: both, Group: "Folder and shell", Label: "Server commands", Control: AgentSettingFile,
			Explanation: "Named commands the sandboxed shell may run on the server itself, each a command line run in the agent's folder with its secrets.",
			Default:     "No server commands.", Allowed: "A map from command name, 1 to 64 letters, digits, _, ., + or -, to a command line."},
		{Key: "allowedNetworkTargets", Modes: both, Group: "Folder and shell", Label: "Network access", Control: AgentSettingList,
			Explanation: "Hosts and web addresses the sandboxed shell's curl and the webfetch tool may reach.",
			Default:     "Public addresses only.", Allowed: "auto, or a list of hosts, host:port entries or URL prefixes; wildcards and IP ranges fail."},
		{Key: "secretsARN", Modes: easy, Group: "Folder and shell", Label: "Secrets", Control: AgentSettingList,
			Explanation: "AWS Secrets Manager secrets fetched each turn and given to server commands as environment variables.",
			Default:     "No secrets.", Allowed: "A list of Secrets Manager ARNs, each secret a JSON object of string values."},
		{Key: "mcp", Modes: easy, Group: "Tools", Label: "Outside tool servers", Control: AgentSettingFile,
			Explanation: "Which configured MCP tool servers the agent may use, and which tool groups (read, write) from each.",
			Default:     "Every tool of every configured server.", Allowed: "auto, {} for none, or a map from server name to auto or a list of its groups, such as [read]."},
		{Key: "rocketclaw", Modes: easy, Group: "Tools", Label: "RocketClaw tools", Control: AgentSettingList,
			Explanation: "Which RocketClaw tool groups the agent gets: sessions, pages, tags, background, workflow and restart.",
			Default:     "All six groups.", Allowed: "auto, or a list of those group names; [] for none."},
		{Key: "tags", Modes: easy, Group: "Tools", Label: "Tags", Control: AgentSettingFile,
			Explanation: "Groups of tags the agent may set on chats. Needed when the RocketClaw tools list includes tags, and only then.",
			Default:     "No tag tools.", Allowed: "A list of lists of tag names."},
		{Key: "skills", Modes: easy, Group: "Tools", Label: "Skills", Control: AgentSettingList,
			Explanation: "Folders under skills/ whose skills, at any depth, the agent may load.",
			Default:     "All skills.", Allowed: "auto, or a list of existing folder names; [] for none."},
		{Key: "decision", Modes: easy, Group: "Tools", Label: "Decision models", Control: AgentSettingList,
			Explanation: "Models the agent's decision tool may ask quick questions, without review.",
			Default:     "No decision tool.", Allowed: "A list of provider/model names whose provider is configured for decisions."},
		{Key: "sudo", Modes: advanced, Group: "Tools", Label: "Full access", Control: AgentSettingSwitch,
			Explanation: "Offers every tool, each call checked first by the guardian reviewer. It replaces permissions.",
			Default:     "Off.", Allowed: "true, false, on, off, yes, no, 1 or 0, in any case."},
		{Key: "permission", Modes: advanced, Group: "Tools", Label: "Permissions", Control: AgentSettingFile, Common: true,
			Explanation: "Which tools the agent may use, by kind of tool and subject: allowed, denied, or reviewed first (auto). Later rules win.",
			Default:     "No tools.", Allowed: "A map from tool kind, such as read, edit, bash or mcp, to a map from subject to allow, deny, auto or auto(agent)."},
		{Key: "permission.rocketclaw", Modes: advanced, Group: "Tools", Label: "RocketClaw permissions", Control: AgentSettingFile,
			Explanation: "Which RocketClaw tools and settings the agent gets, each named exactly.",
			Default:     "Only the RocketClaw tools every agent has.", Allowed: "A map from exact tool or setting name to its value; wildcards fail."},
		{Key: "permission.rocketclaw.rocketclaw_set_tag", Modes: advanced, Group: "Tools", Label: "Tags", Control: AgentSettingFile,
			Explanation: "Groups of tags the agent may set on chats; it may also read tags. It may stay next to full access.",
			Default:     "No tag tools.", Allowed: "allow, deny or auto, or a list of lists of tag names."},
		{Key: "permission.rocketclaw.rocketclaw_sessions_recipient", Modes: advanced, Group: "Who can write", Label: "Who may write to its chats", Control: AgentSettingFile,
			Explanation: "Which agents may start, message or stop this agent's chats. It may stay next to full access.",
			Default:     "Any agent, or none for a full-access agent.", Allowed: "true, false, or a list of agent names without repeats."},
		{Key: "approver", Modes: easy, Group: "Review", Label: "Reviewer", Control: AgentSettingFile,
			Explanation: "How the guardian reviews this agent's calls: its model, extra rules, and a quick decision check before it.",
			Default:     "The guardian on the installation's review model.", Allowed: "A map of model, prompt and decision."},
		{Key: "approver", Modes: advanced, Group: "Review", Label: "As a reviewer", Control: AgentSettingFile,
			Explanation: "Used only when another agent names this one in auto(agent): a quick decision check before this agent reviews.",
			Default:     "No quick decision check.", Allowed: "A map with only decision."},
		{Key: "approver.model", Modes: easy, Group: "Review", Label: "Review model", Control: AgentSettingModel,
			Explanation: "The model the guardian reviews this agent's calls on.",
			Default:     "The installation's review model.", Allowed: "A model name written like the model setting."},
		{Key: "approver.prompt", Modes: easy, Group: "Review", Label: "Review rules", Control: AgentSettingText,
			Explanation: "Rules added to the guardian's own for this agent; they never replace them. Without decision rules too, no quick decision check runs.",
			Default:     "Only the guardian's rules.", Allowed: "Any text."},
		{Key: "approver.decision", Modes: both, Group: "Review", Label: "Quick decision check", Control: AgentSettingFile,
			Explanation: "A decision model asked before the reviewer; a confident answer settles the review.",
			Default:     decisionDefault, Allowed: "A map of model, minConfidence and prompt."},
		{Key: "approver.decision.model", Modes: both, Group: "Review", Label: "Decision model", Control: AgentSettingText,
			Explanation: "The model asked first.",
			Default:     decisionDefault, Allowed: "A provider/model name whose provider is configured for decisions."},
		{Key: "approver.decision.minConfidence", Modes: both, Group: "Review", Label: "Minimum confidence", Control: AgentSettingText,
			Explanation: "How sure the decision model must be before its answer counts.",
			Default:     "The installation's setting, otherwise 0.9.", Allowed: "A number from 0 to 1."},
		{Key: "approver.decision.prompt", Modes: both, Group: "Review", Label: "Decision rules", Control: AgentSettingText,
			Explanation: "Rules only the decision model reads.",
			Default:     "No extra rules.", Allowed: "Any text."},
		{Key: "delegatedFrom", Modes: easy, Group: "Who can write", Label: "Who may write to its chats", Control: AgentSettingFile,
			Explanation: "Which agents may start, message or stop this agent's chats.",
			Default:     "Any agent.", Allowed: "true, false, or a list of agent names."},
		{Key: "routines", Modes: easy, Group: "Routines", Label: "Routines", Control: AgentSettingFile,
			Explanation: "Schedules on which the agent gets a message by itself; a reply opens a Web chat in its space.",
			Default:     "No routines.", Allowed: "A list of schedules (cron, a duration such as 30m, or @daily), each alone or mapped to an instruction; no schedule twice."},
		{Key: "additionalInstructions", Modes: advanced, Group: "Output", Label: "Reply style", Control: AgentSettingText,
			Explanation: "A line added to the agent's prompt about how to write replies, such as avoiding Markdown for Slack.",
			Default:     "Reply in plain text suitable for Slack, avoiding Markdown unless it is necessary.", Allowed: "Any text."},
		{Key: "schema", Modes: both, Group: "Output", Label: "Structured answers", Control: AgentSettingFile,
			Explanation: "Holds the shape every answer must follow.",
			Default:     "Free-form answers.", Allowed: "A map with output."},
		{Key: "schema.output", Modes: both, Group: "Output", Label: "Answer shape", Control: AgentSettingFile,
			Explanation: "A JSON Schema every answer from this agent must match.",
			Default:     "Free-form answers.", Allowed: "A JSON Schema mapping that lists every property under required."},
	}
}

// AgentSettingsFor returns the help for the settings mode lists, in AgentSettings' order.
func AgentSettingsFor(mode AgentMode) []AgentSetting {
	return slices.DeleteFunc(AgentSettings(), func(setting AgentSetting) bool { return !slices.Contains(setting.Modes, mode) })
}
