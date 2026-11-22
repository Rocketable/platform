package harness

import (
	"strconv"

	"go.starlark.net/starlark"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
)

func newBashStarlarkResult(output, errorCode string) starlark.Value {
	details := starlark.StringDict{"output": starlark.String(output), "error_code": starlark.String(errorCode)}
	message := "failed"

	switch _, errNumber := strconv.Atoi(errorCode); {
	case errorCode == "":
		return codemode.NewText("bash_result", output, details, nil)
	case errNumber == nil:
		message = "exit status " + errorCode
	case errorCode == "timeout":
		message = "timed out"
	}

	return codemode.NewFailure("bash", message, details)
}
