package webui

import (
	_ "embed"
	"strings"
)

//go:embed voice_mode.html
var voiceModePageTemplate string

func voiceModePage() string {
	return strings.NewReplacer(
		"__VOICE_MODE_PATH__", VoiceModePath,
		"__KEEPALIVE_PATH__", KeepalivePath,
		"__VOICE_SOCKET_PATH__", voiceSocketPath,
	).Replace(voiceModePageTemplate)
}
