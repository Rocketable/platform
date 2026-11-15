package rpc

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidRequest(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		request *PromptRequest
		refusal string
	}{
		{&PromptRequest{Id: "web-session:sales", Text: "  spaced text stays  ", MessageId: "1:0", AttachmentIds: []string{"a"}}, ""},
		{&PromptRequest{Id: "web-session:sales", Text: "nul\x00"}, "text holds a NUL character"},
		{&PromptRequest{Id: "web-session:sales\n"}, "id has surrounding spaces"},
		{&PromptRequest{Id: strings.Repeat("x", maxIDBytes+1)}, fmt.Sprintf("id is longer than %d bytes", maxIDBytes)},
		{&PromptRequest{Id: "web-session:sales", AttachmentIds: []string{"a", " b"}}, "attachment_ids has surrounding spaces"},
	} {
		err := validRequest(tc.request.ProtoReflect())
		if tc.refusal == "" {
			require.NoError(t, err)

			continue
		}

		require.Equal(t, codes.InvalidArgument, status.Code(err), tc.refusal)
		require.Equal(t, tc.refusal, status.Convert(err).Message())
	}
}

func TestWithoutDatabaseText(t *testing.T) {
	t.Parallel()

	database := fmt.Errorf("read cron run: %w", &pgconn.PgError{Code: "22021", Message: "invalid byte sequence for encoding \"UTF8\""})
	masked := withoutDatabaseText("History", database)
	require.Equal(t, codes.Internal, status.Code(masked))
	require.Equal(t, "web History: rpc error: code = Internal desc = read cron run: database error", status.Convert(masked).Message())

	other := errors.New("revert requires a Web chat, Slack thread, or shadow")
	require.Equal(t, other, withoutDatabaseText("StageRevert", other))
	require.NoError(t, withoutDatabaseText("History", nil))
}
