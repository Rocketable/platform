package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func executeBashScript(command string) map[string]string {
	return map[string]string{"code": "def main():\n    return bash(command=" + strconv.Quote(command) + ")\n"}
}

func writeRawRunFunctionCall(t *testing.T, w http.ResponseWriter, responseID, name string, args any) {
	t.Helper()

	data, err := json.Marshal(args)
	require.NoError(t, err)
	_, err = fmt.Fprintf(w, `{"id":%q,"object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":%q,"type":"function_call","status":"completed","call_id":%q,"name":%q,"arguments":%q}]}`, responseID, "fc_call_1", "call_1", name, string(data))
	require.NoError(t, err)
}

func writeRawRunMessage(t *testing.T, w http.ResponseWriter, responseID, messageID, text string) {
	t.Helper()

	_, err := fmt.Fprintf(w, `{"id":%q,"object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":%q,"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}`, responseID, messageID, text)
	require.NoError(t, err)
}
