package harness

import "slices"

type AgentSettingControl string

const (
	AgentSettingText   AgentSettingControl = "text"
	AgentSettingSwitch AgentSettingControl = "switch"
	AgentSettingChoice AgentSettingControl = "choice"
	AgentSettingList   AgentSettingControl = "list"
	AgentSettingModel  AgentSettingControl = "model"
	AgentSettingFile   AgentSettingControl = "file"
)

type AgentSetting struct {
	Key         string
	Modes       []AgentMode
	Group       string
	Label       string
	Explanation string
	Default     string
	Allowed     string
	Control     AgentSettingControl
	Choices     []string
	Required    bool
	Common      bool
}

func AgentSettings() []AgentSetting {
	easy, advanced, both := []AgentMode{AgentModeEasy}, []AgentMode{AgentModeAdvanced}, []AgentMode{AgentModeEasy, AgentModeAdvanced}

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
		{Key: "sticky", Modes: both, Group: "Basics", Label: "Shared chat", Control: AgentSettingChoice, Choices: []string{"off", "user", "global", "slack"},
			Explanation: "Whether a new chat starts fresh, reopens the person's own chat (user), or reopens one chat everyone shares (global). A slack agent answers only in Slack and is not offered for new chats. A user or slack agent cannot have routines.",
			Default:     "Off: every new chat starts fresh.", Allowed: "off, user, global or slack."},
		{Key: "model", Modes: both, Group: "Model", Label: "Model", Control: AgentSettingModel, Required: true,
			Explanation: "The AI model the agent runs on. Add #low, #medium or #high to set how hard it thinks; without one, it thinks as the model does by default.",
			Default:     "None: every agent must name a model.", Allowed: "A model name, optionally after a configured provider and a slash, optionally followed by #effort."},
		{Key: "reasoningEffort", Modes: advanced, Group: "Model", Label: "Thinking effort (old)", Control: AgentSettingText,
			Explanation: "An older way to set how hard the model thinks. Prefer the #effort ending on the model, which wins over this.",
			Default:     "The model's own default.", Allowed: "An effort name the model accepts, such as low, medium or high."},
		{Key: "verbosity", Modes: advanced, Group: "Model", Label: "Answer length", Control: AgentSettingText,
			Explanation: "How long and detailed the model's answers are.",
			Default:     "The model's own default.", Allowed: "A verbosity the model accepts, such as low, medium or high."},
		{Key: "subagents", Modes: easy, Group: "Helpers", Label: "Helpers", Control: AgentSettingList,
			Explanation: "Models the agent may run copies of itself on with task_clone, and the agents it may hand work to with task. Its RocketClaw sessions tools reach only the chats of agents named here, its own only when it names itself, and the reviewer checks every hand-off to another agent.",
			Default:     "Copies on the agent's own model, and no other agents.", Allowed: "auto, or a list of model names, each written like the model setting, and {agent: name} entries, written - agent: name, each naming one agent; [] for none. A model ending in * allows every model, #effort included, that starts with the text before it; a * anywhere else fails, and agent names take no wildcards. A list without models runs no copies."},
		{Key: "maxRecursion", Modes: both, Group: "Helpers", Label: "Helper depth", Control: AgentSettingText,
			Explanation: "How many levels deep work may be handed down from a chat this agent leads, to copies, other agents and their helpers in turn; the chat's top agent sets the limit for the whole chain, and 0 turns handing off work off.",
			Default:     "1 level.", Allowed: "A whole number, 0 or more."},
		{Key: "workingDirectory", Modes: easy, Group: "Folder and shell", Label: "Folder", Control: AgentSettingText, Required: true,
			Explanation: "The agent's own folder, which its shell and file tools may read and change. It is relative to RocketClaw's folder and may use $VARIABLES.",
			Default:     "None: an easy agent must name a folder.", Allowed: "A folder that is not, does not contain, and is not inside the workspace or RocketClaw's own folders."},
		{Key: "shell", Modes: easy, Group: "Folder and shell", Label: "Shell", Control: AgentSettingText,
			Explanation: "Where the agent's shell commands run: emulated for real Bash in a Linux sandbox, or the full path of a wrapper program on the server. A wrapper gets each command as its one argument, starts in the agent's folder, and gets the agent's secretsARN, commands and allowedNetworkTargets, null where the file leaves a key out, and the folder as workingDirectory, as one JSON object in ROCKETCLAW_ENVIRONMENT.",
			Default:     "The emulated shell.", Allowed: "emulated, or the absolute path of one program without arguments."},
		{Key: "shell", Modes: advanced, Group: "Folder and shell", Label: "Shell", Control: AgentSettingText,
			Explanation: "Where the agent's shell commands run: emulated for real Bash in a Linux sandbox, or the full path of a wrapper program.",
			Default:     "Plain bash on the server.", Allowed: "emulated, or the absolute path of one program without arguments."},
		{Key: "workingDirectory", Modes: advanced, Group: "Folder and shell", Label: "Folder", Control: AgentSettingText,
			Explanation: "With the emulated shell, the agent's own folder, which its shell and file tools may read and change.",
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
			Explanation: "Which configured MCP tool servers the agent may use, and which of each server's facets, the named tool slices rocketclaw.json defines for it.",
			Default:     "No outside tools.", Allowed: "A map from server name to a list of its facets, such as {stripe: [read, write]}."},
		{Key: "opencode", Modes: easy, Group: "Tools", Label: "OpenCode servers", Control: AgentSettingList,
			Explanation: "Configured OpenCode coding servers the agent may call through the opencode_api tool, which can run commands and change files and settings there.",
			Default:     "No opencode_api tool.", Allowed: "A list of server names configured under opencode in rocketclaw.json, such as [build-box]."},
		{Key: "rocketclaw", Modes: easy, Group: "Tools", Label: "RocketClaw tools", Control: AgentSettingList,
			Explanation: "Which RocketClaw tool groups the agent gets: sessions, pages, tags, background and restart. The sessions tools reach only the agents the subagents list names.",
			Default:     "All five groups.", Allowed: "auto, or a list of those group names; [] for none."},
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
			Explanation: "Offers every tool, each call checked first by the guardian reviewer. Permission rules still apply on top of it, later rules winning. A full-access agent's chats are sealed: no agent starts, writes into or reads them, except that full-access agents may read them. It may act on its own chats and on those of every agent without full access.",
			Default:     "Off.", Allowed: "true, false, on, off, yes, no, 1 or 0, in any case."},
		{Key: "permission", Modes: advanced, Group: "Tools", Label: "Permissions", Control: AgentSettingFile, Common: true,
			Explanation: "Which tools the agent may use, by kind of tool and subject: allowed, denied, or reviewed first (auto). Later rules win. With full access they apply on top of it. task rules name the agents it may start with the task tool and, with the RocketClaw sessions tools, whose chats it may read and write; without full access it reaches its own chats only when a task rule names it.",
			Default:     "No tools.", Allowed: "A map from tool kind, such as read, edit or bash, to a map from subject to allow, deny, auto or auto(agent)."},
		{Key: "permission.mcp", Modes: advanced, Group: "Tools", Label: "Outside tool servers", Control: AgentSettingFile,
			Explanation: "Which configured MCP tool servers the agent may use, which of each server's facets, the named tool slices rocketclaw.json defines for it, and whether each facet runs freely or is reviewed first (auto).",
			Default:     "No outside tools, except that full access gets every facet of every server, reviewed.", Allowed: "A map from server name to a map from facet to allow or auto, such as {stripe: {read: allow, write: auto}}."},
		{Key: "permission.rocketclaw", Modes: advanced, Group: "Tools", Label: "RocketClaw permissions", Control: AgentSettingFile,
			Explanation: "Which RocketClaw tools and settings the agent gets, each named exactly. The sessions tools reach only the agents its task rules name.",
			Default:     "Only the RocketClaw tools every agent has.", Allowed: "A map from exact tool or setting name to its value; wildcards fail."},
		{Key: "permission.rocketclaw.rocketclaw_set_tag", Modes: advanced, Group: "Tools", Label: "Tags", Control: AgentSettingFile,
			Explanation: "Groups of tags the agent may set on chats; it may also read tags. It may stay next to full access.",
			Default:     "No tag tools.", Allowed: "allow, deny or auto, or a list of lists of tag names."},
		{Key: "approver", Modes: easy, Group: "Review", Label: "Reviewer", Control: AgentSettingFile,
			Explanation: "How the guardian reviews this agent's calls: its model, extra rules, and tools of its own.",
			Default:     "The guardian on the installation's review model, with only read, glob and grep in the agent's folder.", Allowed: "A map of model, prompt and the reviewer's own subagents, secretsARN, commands, mcp, rocketclaw, tags, skills and allowedNetworkTargets."},
		{Key: "approver.model", Modes: easy, Group: "Review", Label: "Review model", Control: AgentSettingModel,
			Explanation: "The model the guardian reviews this agent's calls on.",
			Default:     "The installation's review model.", Allowed: "A model name written like the model setting."},
		{Key: "approver.prompt", Modes: easy, Group: "Review", Label: "Review rules", Control: AgentSettingText,
			Explanation: "Rules added to the guardian's own for this agent; they never replace them.",
			Default:     "Only the guardian's rules.", Allowed: "Any text."},
		{Key: "approver.subagents", Modes: easy, Group: "Review", Label: "Reviewer's agents", Control: AgentSettingList,
			Explanation: "The agents whose chats the guardian itself may read and write with its RocketClaw sessions tools, in place of the agents the agent's own subagents list names.",
			Default:     "The agents the agent's own subagents list names.", Allowed: "A list of {agent: name} entries, written - agent: name; models and wildcards fail."},
		{Key: "approver.commands", Modes: easy, Group: "Review", Label: "Reviewer's server commands", Control: AgentSettingFile,
			Explanation: "Named commands the guardian itself may run on the server while it reviews. Listing any gives it a shell in the agent's folder. Nothing reviews the guardian's own calls.",
			Default:     "No shell and no server commands.", Allowed: "A map from command name, 1 to 64 letters, digits, _, ., + or -, to a command line."},
		{Key: "approver.allowedNetworkTargets", Modes: easy, Group: "Review", Label: "Reviewer's network access", Control: AgentSettingList,
			Explanation: "Hosts and web addresses the guardian's own webfetch tool and its shell's curl may reach.",
			Default:     "No network access.", Allowed: "auto for public addresses only, or a list of hosts, host:port entries or URL prefixes; wildcards and IP ranges fail."},
		{Key: "approver.secretsARN", Modes: easy, Group: "Review", Label: "Reviewer's secrets", Control: AgentSettingList,
			Explanation: "AWS Secrets Manager secrets fetched each turn and given to the guardian's server commands as environment variables. It never gets the agent's.",
			Default:     "No secrets.", Allowed: "A list of Secrets Manager ARNs, each secret a JSON object of string values."},
		{Key: "approver.mcp", Modes: easy, Group: "Review", Label: "Reviewer's outside tool servers", Control: AgentSettingFile,
			Explanation: "Which configured MCP tool servers the guardian itself may use, and which of each server's facets.",
			Default:     "No outside tool servers.", Allowed: "A map from server name to a list of its facets, such as {stripe: [read]}."},
		{Key: "approver.rocketclaw", Modes: easy, Group: "Review", Label: "Reviewer's RocketClaw tools", Control: AgentSettingList,
			Explanation: "Which RocketClaw tool groups the guardian itself gets: sessions, pages and tags. It writes to other chats under the name guardian.",
			Default:     "None.", Allowed: "auto for all three, or a list of those group names; background and restart fail."},
		{Key: "approver.tags", Modes: easy, Group: "Review", Label: "Reviewer's tags", Control: AgentSettingFile,
			Explanation: "Groups of tags the guardian may set on the chat it reviews. Needed when the reviewer's RocketClaw tools list includes tags, and only then.",
			Default:     "No tag tools.", Allowed: "A list of lists of tag names."},
		{Key: "approver.skills", Modes: easy, Group: "Review", Label: "Reviewer's skills", Control: AgentSettingList,
			Explanation: "Folders under skills/ whose skills, at any depth, the guardian itself may load.",
			Default:     "No skills.", Allowed: "auto for all skills, or a list of existing folder names."},
		{Key: "routines", Modes: both, Group: "Routines", Label: "Routines", Control: AgentSettingFile,
			Explanation: "Schedules on which the agent gets a message by itself; a reply opens a Web chat in its space.",
			Default:     "No routines.", Allowed: "A list of schedules (cron, a duration such as 30m, or @daily), each alone or mapped to an instruction; no schedule twice."},
		{Key: "additionalInstructions", Modes: advanced, Group: "Output", Label: "Reply style", Control: AgentSettingText,
			Explanation: "A line added to the agent's prompt about how to write replies, such as avoiding Markdown for Slack.",
			Default:     "No reply-style line.", Allowed: "Any text."},
		{Key: "schema", Modes: both, Group: "Output", Label: "Structured answers", Control: AgentSettingFile,
			Explanation: "Holds the shape every answer must follow.",
			Default:     "Free-form answers.", Allowed: "A map with output."},
		{Key: "schema.output", Modes: both, Group: "Output", Label: "Answer shape", Control: AgentSettingFile,
			Explanation: "A JSON Schema every answer from this agent must match.",
			Default:     "Free-form answers.", Allowed: "A JSON Schema mapping that lists every property under required."},
	}
}

func AgentSettingsFor(mode AgentMode) []AgentSetting {
	return slices.DeleteFunc(AgentSettings(), func(setting AgentSetting) bool { return !slices.Contains(setting.Modes, mode) })
}
