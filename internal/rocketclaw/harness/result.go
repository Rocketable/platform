package harness

import "github.com/Rocketable/platform/internal/rocketclaw/codemode"

func codeModeFileArgs(tool string) []string {
	switch tool {
	case "bash":
		return []string{"stdin"}
	case "decision":
		return []string{"images[]"}
	default:
		return nil
	}
}

func newFileValue(spec codemode.FileSpec) *codemode.File {
	return codemode.NewFile(spec, imageOps{})
}
