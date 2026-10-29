package rpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// maxIDBytes bounds an ID a request names; stored IDs are far shorter.
const maxIDBytes = 1024

// Register publishes the implemented portion of rpc.Web. Other methods retain
// gRPC's Unimplemented response; they are not successful empty handlers.
func (s *Server) Register(registrar grpc.ServiceRegistrar) {
	desc := grpc.ServiceDesc{ServiceName: "rpc.Web", HandlerType: (*any)(nil), Metadata: "web.proto"}

	for _, method := range []string{"Protocol", "Identity", "Reload", "Prompt", "History", "ForkSession", "StageRevert", "ClearRevert", "SearchMessages", "SearchSessions", "SlackNames", "Handoff", "ListAgents", "CreateSession", "ListConfig", "ListSkills", "ListWorkflows", "UpdateSession", "ListCronJobs", "RunCronJob", "ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries", "ListQueue", "SteerQueueItem", "PopQueueItem", "RemoveQueueItem", "EditQueueItem", "ReorderQueue", "MoveToBackground", "StopBackgroundJob", "AnswerQuestion", "VoiceSettings", "Tabs", "GetPage", "ListPages"} {
		descriptor := File_web_proto.Services().ByName("Web").Methods().ByName(protoreflect.Name(method))
		requestType, _ := protoregistry.GlobalTypes.FindMessageByName(descriptor.Input().FullName())

		desc.Methods = append(desc.Methods, grpc.MethodDesc{MethodName: method, Handler: func(_ any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			request := requestType.New().Interface()

			if err := decode(request); err != nil {
				return nil, err
			}

			if err := validRequest(request.ProtoReflect()); err != nil {
				return nil, err
			}

			handler := func(ctx context.Context, request any) (any, error) {
				response, err := s.webCall(ctx, method, request)

				return response, withoutDatabaseText(method, err)
			}
			if interceptor != nil {
				return interceptor(ctx, request, &grpc.UnaryServerInfo{Server: s, FullMethod: "/rpc.Web/" + method}, handler)
			}

			return handler(ctx, request)
		}})
	}

	desc.Streams = []grpc.StreamDesc{{StreamName: "UploadAttachment", ClientStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		return withoutDatabaseText("UploadAttachment", s.uploadAttachment(stream))
	}}, {StreamName: "DownloadAttachment", ServerStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		return withoutDatabaseText("DownloadAttachment", s.downloadAttachment(stream))
	}}, {StreamName: "Join", ServerStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		request := &JoinRequest{}
		if err := stream.RecvMsg(request); err != nil {
			return fmt.Errorf("receive web join: %w", err)
		}

		if err := validRequest(request.ProtoReflect()); err != nil {
			return err
		}

		return withoutDatabaseText("Join", s.join(request, stream))
	}}, {StreamName: "Voice", ServerStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		request := &VoiceRequest{}
		if err := stream.RecvMsg(request); err != nil {
			return fmt.Errorf("receive web voice: %w", err)
		}

		if err := validRequest(request.ProtoReflect()); err != nil {
			return err
		}

		return withoutDatabaseText("Voice", s.voice(request, stream))
	}}, {StreamName: "ListSessions", ServerStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		request := &ListSessionsRequest{}
		if err := stream.RecvMsg(request); err != nil {
			return fmt.Errorf("receive web session list: %w", err)
		}

		if err := validRequest(request.ProtoReflect()); err != nil {
			return err
		}

		return withoutDatabaseText("ListSessions", s.listSessions(request, stream))
	}}}
	registrar.RegisterService(&desc, s)
}

// validRequest refuses a request holding a string no stored value may: one with a NUL character,
// which PostgreSQL text cannot hold, or an ID, a field named id or ending in _id or _ids, with
// surrounding spaces or over maxIDBytes, so a lookup never trims or truncates it into another.
func validRequest(message protoreflect.Message) error {
	var err error

	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.IsList():
			for i := range value.List().Len() {
				if err = validValue(field, value.List().Get(i)); err != nil {
					return false
				}
			}
		case field.IsMap():
			value.Map().Range(func(key protoreflect.MapKey, value protoreflect.Value) bool {
				err = errors.Join(validValue(field.MapKey(), key.Value()), validValue(field.MapValue(), value))

				return err == nil
			})
		default:
			err = validValue(field, value)
		}

		return err == nil
	})

	return err
}

func validValue(field protoreflect.FieldDescriptor, value protoreflect.Value) error {
	if field.Kind() == protoreflect.MessageKind {
		return validRequest(value.Message())
	}

	if field.Kind() != protoreflect.StringKind {
		return nil
	}

	name, text := string(field.Name()), value.String()
	id := name == "id" || strings.HasSuffix(name, "_id") || strings.HasSuffix(name, "_ids")

	switch {
	case strings.ContainsRune(text, 0):
		return status.Errorf(codes.InvalidArgument, "%s holds a NUL character", name)
	case id && len(text) > maxIDBytes:
		return status.Errorf(codes.InvalidArgument, "%s is longer than %d bytes", name, maxIDBytes)
	case id && strings.TrimSpace(text) != text:
		return status.Errorf(codes.InvalidArgument, "%s has surrounding spaces", name)
	}

	return nil
}

// withoutDatabaseText keeps a database error's own text, which can name tables and values, out of
// the response of method: the caller gets Internal with what the server was doing, and the log the
// whole error.
func withoutDatabaseText(method string, err error) error {
	if errPg, ok := errors.AsType[*pgconn.PgError](err); ok {
		slog.Error("web call failed in the database", "method", method, "error", err)

		return fmt.Errorf("web %s: %w", method, status.Error(codes.Internal, strings.TrimSuffix(err.Error(), errPg.Error())+"database error"))
	}

	return err
}

func (s *Server) webCall(ctx context.Context, method string, request any) (any, error) {
	switch method {
	case "Reload":
		return s.reload(ctx, request.(*ReloadRequest))
	case "ListCronJobs":
		return s.listCronJobs(ctx)
	case "RunCronJob":
		return s.runCronJob(ctx, request.(*RunCronJobRequest))
	case "UpdateSession":
		return s.updateSession(ctx, request.(*UpdateSessionRequest))
	case "ListConfig":
		return s.listConfig(ctx)
	case "ListSkills":
		return s.listSkills(ctx, request.(*ListSkillsRequest))
	case "ListWorkflows":
		return s.listWorkflows(ctx)
	case "ListAgents":
		return s.listAgents(ctx, request.(*ListAgentsRequest).ConversationId)
	case "Identity":
		return s.identity(ctx)
	case "CreateSession":
		return s.createSession(ctx, request.(*CreateSessionRequest))
	case "VoiceSettings":
		return s.voiceSettings(ctx, request.(*VoiceSettingsRequest))
	case "History":
		return s.historyRPC(ctx, request.(*HistoryRequest))
	case "ForkSession":
		return s.forkSession(ctx, request.(*ForkSessionRequest))
	case "StageRevert":
		return s.stageRevert(ctx, request.(*StageRevertRequest))
	case "ClearRevert":
		return s.clearRevert(ctx, request.(*ClearRevertRequest))
	case "SearchMessages":
		return s.searchMessages(ctx, request.(*SearchMessagesRequest))
	case "SearchSessions":
		return s.searchSessions(ctx, request.(*SearchSessionsRequest))
	case "SlackNames":
		return s.slackNames(ctx, request.(*SlackNamesRequest))
	case "Handoff":
		return s.handoff(ctx, request.(*HandoffRequest))
	case "Prompt":
		return s.prompt(ctx, request.(*PromptRequest))
	case "ListQueue":
		return s.listQueue(ctx, request.(*ListQueueRequest))
	case "SteerQueueItem", "PopQueueItem", "RemoveQueueItem", "EditQueueItem":
		return s.queueItem(ctx, method, request.(*QueueItemRequest))
	case "ReorderQueue":
		return s.reorderQueue(ctx, request.(*ReorderQueueRequest))
	case "MoveToBackground":
		return s.moveToBackground(ctx, request.(*MoveToBackgroundRequest))
	case "StopBackgroundJob":
		return s.stopBackgroundJob(ctx, request.(*StopBackgroundJobRequest))
	case "AnswerQuestion":
		return s.answerQuestion(ctx, request.(*AnswerQuestionRequest))
	case "ListSessionEntries":
		return s.ListSessionEntries(ctx, request.(*SessionEntriesRequest))
	case "LoadSessionEntries":
		return s.LoadSessionEntries(ctx, request.(*SessionEntriesRequest))
	case "DeleteSessionEntries":
		return s.DeleteSessionEntries(ctx, request.(*SessionEntriesRequest))
	default:
		return s.pageCall(ctx, request)
	}
}

func (s *Server) identity(ctx context.Context) (*IdentityResponse, error) {
	username, principal, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}

	return &IdentityResponse{Username: username, Principal: principal, JustBashError: s.backend.JustBashInstallError()}, nil
}

func (s *Server) reload(ctx context.Context, request *ReloadRequest) (*ReloadResponse, error) {
	if _, _, err := s.principal(ctx); err != nil {
		return nil, err
	}

	message, err := s.backend.Reload(request.Reason)
	if err != nil {
		return nil, fmt.Errorf("web reload: %w", err)
	}

	return &ReloadResponse{Message: message}, nil
}

// historyRPC answers the History RPC: the transcript page plus the conversation's Background
// Jobs and pending Web questions, which internal history reads (search, attachments, session
// commands) do not need.
func (s *Server) historyRPC(ctx context.Context, request *HistoryRequest) (*HistoryResponse, error) {
	response, err := s.history(ctx, request)
	if err != nil {
		return nil, err
	}

	if err := s.listPending(ctx, response, request); err != nil {
		return nil, err
	}

	return response, nil
}
