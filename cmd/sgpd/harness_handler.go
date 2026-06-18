package main

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	sgpv1 "github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1"
	"github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1/sgpv1connect"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/store/sgpd/convert"
)

type harnessHandler struct {
	sgpv1connect.UnimplementedSGPHarnessServiceHandler

	store sgp.Store
}

func (h *harnessHandler) AppendEvent(
	ctx context.Context,
	req *connect.Request[sgpv1.AppendEventRequest],
) (*connect.Response[sgpv1.AppendEventResponse], error) {
	if req.Msg.GetSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errSessionIDRequired)
	}

	if req.Msg.GetEvent() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errEventRequired)
	}

	event := convert.EventFromProto(req.Msg.GetEvent())
	sessionID := sgp.ID(req.Msg.GetSessionId())

	var err error

	switch sgp.ClassifyEvent(event) {
	case sgp.EventKindSessionStart:
		sess := sgp.Session{
			ID:          sessionID,
			Timestamp:   event.Timestamp,
			SpawnedFrom: event.SpawnedFrom,
		}
		err = h.store.CreateSession(ctx, sess)
	case sgp.EventKindNodeAppended, sgp.EventKindHistoryRewritten:
		if event.Node == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("event missing node"))
		}
		err = h.store.WriteNode(ctx, *event.Node)
	case sgp.EventKindSessionEnded:
		err = h.store.EndSession(ctx, sessionID, event.Reason, event.TerminalNodeID)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown event kind"))
	}

	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("store: %w", err))
	}

	return connect.NewResponse(&sgpv1.AppendEventResponse{}), nil
}

func (h *harnessHandler) LoadEvents(
	ctx context.Context,
	req *connect.Request[sgpv1.LoadEventsRequest],
) (*connect.Response[sgpv1.LoadEventsResponse], error) {
	if req.Msg.GetSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errSessionIDRequired)
	}

	g, err := h.store.LoadGraph(ctx, sgp.ID(req.Msg.GetSessionId()))
	if err != nil {
		if errors.Is(err, sgp.ErrSessionNotFound) || errors.Is(err, sgp.ErrGraphNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}

		return nil, connect.NewError(connect.CodeInternal, err)
	}

	events := sgp.SynthesizeEvents(g)
	pbEvents := make([]*sgpv1.Event, len(events))

	for i, e := range events {
		pbEvents[i] = convert.EventToProto(e)
	}

	return connect.NewResponse(&sgpv1.LoadEventsResponse{Events: pbEvents}), nil
}
