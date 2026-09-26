package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Listen opens a Unix socket inside an existing private directory. Only the
// trusted Web proxy, running as the same OS user, may supply browser IP metadata.
// TCP and forwarded HTTP headers are not authentication boundaries.
func Listen(socketPath string) (net.Listener, error) {
	if !filepath.IsAbs(socketPath) {
		return nil, errors.New("web RPC socket path must be absolute")
	}

	dir, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		return nil, fmt.Errorf("stat web RPC socket directory: %w", err)
	}

	if !dir.IsDir() || dir.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("web RPC socket directory must be private (0700)")
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on web RPC socket: %w", err)
	}

	return listener, nil
}

// Register publishes the implemented portion of rpc.Web. Other methods retain
// gRPC's Unimplemented response; they are not successful empty handlers.
func (s *Server) Register(registrar grpc.ServiceRegistrar) {
	desc := grpc.ServiceDesc{ServiceName: "rpc.Web", HandlerType: (*any)(nil), Metadata: "web.proto"}

	for _, method := range []string{"Protocol", "Identity", "Prompt", "History", "ForkSession", "SearchMessages", "Handoff", "ListAgents", "CreateSession", "ListConfig", "ListSkills", "SettleSession", "UpdateSession", "ListCronJobs", "RunCronJob", "ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries", "ListQueue", "SteerQueueItem", "PopQueueItem", "RemoveQueueItem", "ReorderQueue"} {
		descriptor := File_web_proto.Services().ByName("Web").Methods().ByName(protoreflect.Name(method))
		requestType, _ := protoregistry.GlobalTypes.FindMessageByName(descriptor.Input().FullName())

		desc.Methods = append(desc.Methods, grpc.MethodDesc{MethodName: method, Handler: func(_ any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			request := requestType.New().Interface()

			if err := decode(request); err != nil {
				return nil, err
			}

			handler := func(ctx context.Context, request any) (any, error) {
				return s.webCall(ctx, method, request)
			}
			if interceptor != nil {
				return interceptor(ctx, request, &grpc.UnaryServerInfo{Server: s, FullMethod: "/rpc.Web/" + method}, handler)
			}

			return handler(ctx, request)
		}})
	}

	desc.Streams = []grpc.StreamDesc{{StreamName: "UploadAttachment", ClientStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		return s.uploadAttachment(stream)
	}}, {StreamName: "DownloadAttachment", ServerStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		return s.downloadAttachment(stream)
	}}, {StreamName: "Join", ServerStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		request := &JoinRequest{}
		if err := stream.RecvMsg(request); err != nil {
			return fmt.Errorf("receive web join: %w", err)
		}

		return s.join(request, stream)
	}}, {StreamName: "ListSessions", ServerStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		if err := stream.RecvMsg(&ListSessionsRequest{}); err != nil {
			return fmt.Errorf("receive web session list: %w", err)
		}

		return s.listSessions(stream)
	}}}
	registrar.RegisterService(&desc, s)
}

func (s *Server) webCall(ctx context.Context, method string, request any) (any, error) {
	switch method {
	case "ListCronJobs":
		return s.listCronJobs(ctx)
	case "RunCronJob":
		return s.runCronJob(ctx, request.(*RunCronJobRequest))
	case "SettleSession":
		return s.settleSession(ctx, request.(*SettleSessionRequest))
	case "UpdateSession":
		return s.updateSession(ctx, request.(*UpdateSessionRequest))
	case "ListConfig":
		return s.listConfig(ctx)
	case "ListSkills":
		return s.listSkills(ctx, request.(*ListSkillsRequest))
	case "ListAgents":
		return s.listAgents(ctx, request.(*ListAgentsRequest).ConversationId)
	case "Identity":
		username, err := s.principal(ctx)
		if err != nil {
			return nil, err
		}

		return &IdentityResponse{Username: username}, nil
	case "CreateSession":
		return s.createSession(ctx, request.(*CreateSessionRequest))
	case "History":
		return s.history(ctx, request.(*HistoryRequest))
	case "ForkSession":
		return s.forkSession(ctx, request.(*ForkSessionRequest))
	case "SearchMessages":
		return s.searchMessages(ctx, request.(*SearchMessagesRequest))
	case "Handoff":
		return s.handoff(ctx, request.(*HandoffRequest))
	case "Prompt":
		return s.prompt(ctx, request.(*PromptRequest))
	case "ListQueue":
		return s.listQueue(ctx, request.(*ListQueueRequest))
	case "SteerQueueItem", "PopQueueItem", "RemoveQueueItem":
		return s.queueItem(ctx, method, request.(*QueueItemRequest))
	case "ReorderQueue":
		return s.reorderQueue(ctx, request.(*ReorderQueueRequest))
	case "ListSessionEntries":
		return s.ListSessionEntries(ctx, request.(*SessionEntriesRequest))
	case "LoadSessionEntries":
		return s.LoadSessionEntries(ctx, request.(*SessionEntriesRequest))
	case "DeleteSessionEntries":
		return s.DeleteSessionEntries(ctx, request.(*SessionEntriesRequest))
	default: // Protocol negotiates the schema, not a browser principal.
		return &ProtocolResponse{ProtoSha256: protoSHA256}, nil
	}
}
