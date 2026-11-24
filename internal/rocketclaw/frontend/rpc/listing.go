package rpc

import (
	"fmt"

	"google.golang.org/grpc"
)

func (s *Server) watchListing(_ *WatchListingRequest, stream grpc.ServerStream) error {
	ctx := stream.Context()

	owner, _, err := s.spaceLimit(ctx)
	if err != nil {
		return err
	}

	changed, stop := s.sessions.WatchListing(owner)
	defer stop()

	for {
		if err := stream.SendMsg(&ListingChanged{}); err != nil {
			return fmt.Errorf("send web listing change: %w", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		}
	}
}
