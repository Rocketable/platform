package slackconnector

import "strings"

const (
	slackRobotReaction                         = "robot_face"
	slackExternalMCPRelayReaction              = "satellite_antenna"
	slackBufferedReaction                      = "hourglass_flowing_sand"
	slackGoalStopSignReaction                  = "octagonal_sign"
	slackGoalStopButtonReaction                = "stop_button"
	slackGoalCompleteReaction                  = "white_check_mark"
	slackInterruptionReaction                  = "exclamation"
	slackEnvelopeReaction                      = "envelope"
	slackFastUpButtonReaction                  = "fast_up_button"
	slackBlackUpPointingDoubleTriangleReaction = "black_up_pointing_double_triangle"
	slackArrowDoubleUpReaction                 = "arrow_double_up"
)

func slackEmojiGlyph(name string) (string, bool) {
	switch name {
	case "repeat":
		return "🔁", true
	case "checkered_flag":
		return "🏁", true
	case slackGoalStopSignReaction:
		return "🛑", true
	case slackGoalStopButtonReaction:
		return "⏹️", true
	case "repeat_one":
		return "🔂", true
	case "fast_forward_button":
		return "⏩", true
	case "control_knobs":
		return "🎛️", true
	case slackBufferedReaction:
		return "⏳", true
	case slackEnvelopeReaction:
		return "✉️", true
	case "calendar":
		return "📅", true
	case slackRobotReaction:
		return "🤖", true
	case slackExternalMCPRelayReaction:
		return "📡", true
	case slackGoalCompleteReaction:
		return "✅", true
	case slackInterruptionReaction:
		return "❗", true
	case slackArrowDoubleUpReaction, slackFastUpButtonReaction, slackBlackUpPointingDoubleTriangleReaction:
		return "⏫", true
	case "incoming_envelope":
		return "📨", true
	default:
		return "", false
	}
}

func canonicalizeLeadingSlackEmoji(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, ":") {
		return text
	}

	end := strings.Index(text[1:], ":")
	if end < 0 {
		return text
	}

	glyph, ok := slackEmojiGlyph(text[1 : end+1])
	if !ok {
		return text
	}

	rest := strings.TrimSpace(text[end+2:])
	if rest == "" {
		return glyph
	}

	return glyph + " " + rest
}
