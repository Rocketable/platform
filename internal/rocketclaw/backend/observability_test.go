package backend

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	instrumentation "github.com/Arize-ai/openinference/go/openinference-instrumentation"
	semconv "github.com/Arize-ai/openinference/go/openinference-semantic-conventions"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRunTurnSpanPreservesMaskingAndFailure(t *testing.T) {
	for _, test := range []struct {
		name                    string
		hideInputs, hideOutputs bool
		fail                    bool
		input, output           string
	}{
		{"visible", false, false, false, "private input", "answer"},
		{"hide input", true, false, false, instrumentation.RedactedValue, "answer"},
		{"hide output", false, true, false, "private input", instrumentation.RedactedValue},
		{"hide both", true, true, false, instrumentation.RedactedValue, instrumentation.RedactedValue},
		{"failure", false, false, true, "private input", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			previous := otel.GetTracerProvider()

			otel.SetTracerProvider(provider)
			t.Cleanup(func() {
				otel.SetTracerProvider(previous)
				require.NoError(t, provider.Shutdown(context.Background()))
			})

			nt := newNoteTest(t, answerNoteTest)

			nt.cfg.Instrumentation = config.InstrumentationConfig{Enabled: true, HideInputs: test.hideInputs, HideOutputs: test.hideOutputs}
			if test.fail {
				nt.cfg.Workspace = filepath.Join(nt.cfg.Workspace, "missing")
			}

			questions := &webQuestions{store: nt.service, waiting: map[webQuestionKey]chan protocol.AskUserQuestionAnswer{}}
			bridge := NewConversation(config.NewLockedConfig(nt.cfg), finalsPublisher{finals: nt.finals}, &Config{ConversationID: nt.conversationID, Agent: "main", SessionService: nt.service, RequestRestart: testNoopRestart, RequestReload: testNoopRestart, UserQuestionAsker: questions, StartNewThread: testNoopStartNewThread}, slog.New(slog.DiscardHandler))

			_, err := runTestTurn(t.Context(), bridge, protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "private input", false), "span-turn")
			if !test.fail {
				require.NoError(t, err)
			}

			ended := 0

			for _, span := range recorder.Ended() {
				if span.Name() != "rocketclaw.turn" {
					continue
				}

				ended++
				attributes := attribute.NewSet(span.Attributes()...)
				input, found := attributes.Value(attribute.Key(semconv.InputValue))
				require.True(t, found)
				output, found := attributes.Value(attribute.Key(semconv.OutputValue))
				require.True(t, found)
				require.Equal(t, test.input, input.AsString())
				require.Equal(t, test.output, output.AsString())

				if test.fail {
					require.ErrorContains(t, err, "open workspace root")
					require.Equal(t, sdktrace.Status{Code: codes.Error, Description: err.Error()}, span.Status())
					require.Len(t, span.Events(), 1)
					require.Equal(t, "exception", span.Events()[0].Name)
				}
			}

			require.Equal(t, 1, ended)
		})
	}
}
