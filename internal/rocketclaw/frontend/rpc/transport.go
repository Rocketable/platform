package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
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
	for _, method := range []string{"Protocol", "Prompt", "History", "ListSessions", "ListAgents", "CreateSession", "ListConfig", "ListSkills", "SettleSession", "ListCronJobs", "RunCronJob", "ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries", "ListQueue", "SteerQueueItem", "RemoveQueueItem", "ReorderQueue"} {
		desc.Methods = append(desc.Methods, grpc.MethodDesc{MethodName: method, Handler: func(_ any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			var request proto.Message = &SessionEntriesRequest{}

			switch method {
			case "ListCronJobs":
				request = &ListCronJobsRequest{}
			case "RunCronJob":
				request = &RunCronJobRequest{}
			case "SettleSession":
				request = &SettleSessionRequest{}
			case "ListConfig":
				request = &ListConfigRequest{}
			case "ListSkills":
				request = &ListSkillsRequest{}
			case "ListAgents":
				request = &ListAgentsRequest{}
			case "CreateSession":
				request = &CreateSessionRequest{}
			case "ListSessions":
				request = &ListSessionsRequest{}
			case "History":
				request = &HistoryRequest{}
			case "Protocol":
				request = &ProtocolRequest{}
			case "Prompt":
				request = &PromptRequest{}
			case "ListQueue":
				request = &ListQueueRequest{}
			case "SteerQueueItem", "RemoveQueueItem":
				request = &QueueItemRequest{}
			case "ReorderQueue":
				request = &ReorderQueueRequest{}
			}

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

	desc.Streams = []grpc.StreamDesc{{StreamName: "Join", ServerStreams: true, Handler: func(_ any, stream grpc.ServerStream) error {
		request := &JoinRequest{}
		if err := stream.RecvMsg(request); err != nil {
			return fmt.Errorf("receive web join: %w", err)
		}

		return s.join(request, stream)
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
	case "ListConfig":
		return s.listConfig(ctx)
	case "ListSkills":
		return s.listSkills(ctx)
	case "ListAgents":
		return s.listAgents(ctx)
	case "CreateSession":
		return s.createSession(ctx, request.(*CreateSessionRequest))
	case "ListSessions":
		return s.listSessions(ctx)
	case "History":
		return s.history(ctx, request.(*HistoryRequest))
	case "Prompt":
		return s.prompt(ctx, request.(*PromptRequest))
	case "ListQueue":
		return s.listQueue(ctx, request.(*ListQueueRequest))
	case "SteerQueueItem":
		return s.steerQueueItem(ctx, request.(*QueueItemRequest))
	case "RemoveQueueItem":
		return s.removeQueueItem(ctx, request.(*QueueItemRequest))
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
