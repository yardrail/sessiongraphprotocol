package main

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	sgpv1 "github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1"
	"github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1/sgpv1connect"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	cayleystore "github.com/restrukt-ai/sessiongraphprotocol/pkg/store/cayley"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/store/sgpd/convert"
)

type managementHandler struct {
	sgpv1connect.UnimplementedSGPManagementServiceHandler

	store sgp.Store
}

func (h *managementHandler) ListSessions(
	ctx context.Context,
	req *connect.Request[sgpv1.ListSessionsRequest],
) (*connect.Response[sgpv1.ListSessionsResponse], error) {
	sessions, nextToken, err := h.store.ListSessions(
		ctx,
		req.Msg.GetPageToken(),
		int(req.Msg.GetLimit()),
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	pbSessions := make([]*sgpv1.Session, len(sessions))

	for i, s := range sessions {
		pbSessions[i] = convert.SessionToProto(s)
	}

	return connect.NewResponse(&sgpv1.ListSessionsResponse{
		Sessions:      pbSessions,
		NextPageToken: nextToken,
	}), nil
}

func (h *managementHandler) GetSession(
	ctx context.Context,
	req *connect.Request[sgpv1.GetSessionRequest],
) (*connect.Response[sgpv1.GetSessionResponse], error) {
	if req.Msg.GetSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errSessionIDRequired)
	}

	sessionID := sgp.ID(req.Msg.GetSessionId())

	sess, status, err := h.store.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sgp.ErrSessionNotFound) || errors.Is(err, sgp.ErrGraphNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}

		return nil, connect.NewError(connect.CodeInternal, err)
	}

	pbStatus := sgpv1.SessionStatus_SESSION_STATUS_OPEN
	if status == sgp.SessionStatusClosed {
		pbStatus = sgpv1.SessionStatus_SESSION_STATUS_CLOSED
	}

	// Get head node ID via LoadGraph.
	var headID sgp.ID
	if g, gErr := h.store.LoadGraph(ctx, sessionID); gErr == nil {
		if head, ok := g.Head(); ok {
			headID = head.ID
		}
	}

	return connect.NewResponse(&sgpv1.GetSessionResponse{
		Session:    convert.SessionToProto(sess),
		HeadNodeId: string(headID),
		Status:     pbStatus,
	}), nil
}

func (h *managementHandler) GetNode(
	ctx context.Context,
	req *connect.Request[sgpv1.GetNodeRequest],
) (*connect.Response[sgpv1.GetNodeResponse], error) {
	if req.Msg.GetNodeId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errNodeIDRequired)
	}

	node, err := h.store.GetNode(ctx, sgp.ID(req.Msg.GetNodeId()))
	if err != nil {
		if errors.Is(err, sgp.ErrNodeNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}

		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&sgpv1.GetNodeResponse{Node: convert.NodeToProto(node)}), nil
}

func (h *managementHandler) GetResumeContext(
	ctx context.Context,
	req *connect.Request[sgpv1.GetResumeContextRequest],
) (*connect.Response[sgpv1.GetResumeContextResponse], error) {
	if req.Msg.GetNodeId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errNodeIDRequired)
	}

	nodes, err := h.store.GetLineage(ctx, sgp.ID(req.Msg.GetNodeId()))
	if err != nil {
		if errors.Is(err, sgp.ErrNodeNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}

		return nil, connect.NewError(connect.CodeInternal, err)
	}

	pbNodes := make([]*sgpv1.Node, len(nodes))
	pbMsgs := make([]*sgpv1.Message, len(nodes))

	for i, n := range nodes {
		pbNodes[i] = convert.NodeToProto(n)
		pbMsgs[i] = convert.MessageToProto(n.Message)
	}

	return connect.NewResponse(&sgpv1.GetResumeContextResponse{
		Nodes:    pbNodes,
		Messages: pbMsgs,
	}), nil
}

// sessionGrapher is the optional interface for stores that can return session graph data.
type sessionGrapher interface {
	GetSessionGraph(ctx context.Context, sessionID sgp.ID) ([]sgp.Node, []cayleystore.Edge, error)
}

func (h *managementHandler) GetSessionGraph(
	ctx context.Context,
	req *connect.Request[sgpv1.GetSessionGraphRequest],
) (*connect.Response[sgpv1.GetSessionGraphResponse], error) {
	if req.Msg.GetSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errSessionIDRequired)
	}

	sg, ok := h.store.(sessionGrapher)
	if !ok {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("GetSessionGraph not supported by this store"))
	}

	nodes, edges, err := sg.GetSessionGraph(ctx, sgp.ID(req.Msg.GetSessionId()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	pbNodes := make([]*sgpv1.Node, len(nodes))

	for i, n := range nodes {
		pbNodes[i] = convert.NodeToProto(n)
	}

	pbEdges := make([]*sgpv1.NodeEdge, len(edges))

	for i, e := range edges {
		pbEdges[i] = &sgpv1.NodeEdge{FromId: string(e.FromID), ToId: string(e.ToID), Kind: e.Kind}
	}

	return connect.NewResponse(&sgpv1.GetSessionGraphResponse{Nodes: pbNodes, Edges: pbEdges}), nil
}

func (h *managementHandler) WatchSession(
	ctx context.Context,
	req *connect.Request[sgpv1.WatchSessionRequest],
	stream *connect.ServerStream[sgpv1.SessionObservation],
) error {
	if req.Msg.GetSessionId() == "" {
		return connect.NewError(connect.CodeInvalidArgument, errSessionIDRequired)
	}

	watcher, ok := h.store.(sgp.Watcher)
	if !ok {
		return connect.NewError(connect.CodeUnimplemented, errors.New("watch not supported by this store"))
	}

	sessionID := sgp.ID(req.Msg.GetSessionId())

	// Subscribe BEFORE loading history to avoid missing live events.
	nodeCh, cancel, err := watcher.Watch(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sgp.ErrSessionNotFound) {
			return connect.NewError(connect.CodeNotFound, err)
		}

		return connect.NewError(connect.CodeInternal, err)
	}
	defer cancel()

	// Replay history if requested.
	if req.Msg.GetReplayHistory() {
		g, loadErr := h.store.LoadGraph(ctx, sessionID)
		if loadErr != nil && !errors.Is(loadErr, sgp.ErrSessionNotFound) && !errors.Is(loadErr, sgp.ErrGraphNotFound) {
			return connect.NewError(connect.CodeInternal, loadErr)
		}

		if g != nil {
			for _, e := range sgp.SynthesizeEvents(g) {
				if sendErr := stream.Send(eventToObservation(e)); sendErr != nil {
					return sendErr
				}
			}
		}
	}

	// Stream live nodes.
	for {
		select {
		case <-ctx.Done():
			return nil
		case node, ok := <-nodeCh:
			if !ok {
				return nil
			}

			e := nodeToEvent(node)
			if sendErr := stream.Send(eventToObservation(e)); sendErr != nil {
				return sendErr
			}
		}
	}
}

func nodeToEvent(node sgp.Node) sgp.Event {
	n := node

	return sgp.Event{
		Kind:      sgp.EventKindNodeAppended,
		Event:     sgp.DefaultEventNames().NodeAppended,
		SessionID: node.SessionID,
		Timestamp: node.Timestamp,
		Node:      &n,
	}
}

func eventToObservation(e sgp.Event) *sgpv1.SessionObservation {
	pbStatus := sgpv1.SessionStatus_SESSION_STATUS_OPEN
	pbReason := sgpv1.EndReason_END_REASON_UNSPECIFIED

	var headID sgp.ID
	if e.Node != nil {
		headID = e.Node.ID
	}

	if e.Kind == sgp.EventKindSessionEnded {
		pbStatus = sgpv1.SessionStatus_SESSION_STATUS_CLOSED
		pbReason = protoEndReason(e.Reason)
		headID = e.TerminalNodeID
	}

	return &sgpv1.SessionObservation{
		Event:      convert.EventToProto(e),
		HeadNodeId: string(headID),
		Status:     pbStatus,
		EndReason:  pbReason,
	}
}

func protoEndReason(r sgp.EndReason) sgpv1.EndReason {
	switch r {
	case sgp.EndReasonComplete:
		return sgpv1.EndReason_END_REASON_COMPLETE
	case sgp.EndReasonFailed:
		return sgpv1.EndReason_END_REASON_FAILED
	default:
		return sgpv1.EndReason_END_REASON_UNSPECIFIED
	}
}

var _ sgpv1connect.SGPManagementServiceHandler = (*managementHandler)(nil)
