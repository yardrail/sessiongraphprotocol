// Package sgpd provides a gRPC client store backed by the sgpd service.
package sgpd

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	sgpv1 "github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1"
	"github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1/sgpv1connect"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/store/sgpd/convert"
)

// ErrUnsupported is returned by methods that require the management client which is not wired.
var ErrUnsupported = errors.New("operation not supported by this store")

// Client implements sgp.Store backed by the SGPHarnessService RPC.
// It connects over h2c (HTTP/2 cleartext) with bearer token auth.
type Client struct {
	rpc sgpv1connect.SGPHarnessServiceClient
}

var _ sgp.Store = (*Client)(nil)

// NewClient constructs a Client for the given sgpd base URL.
// baseURL should be e.g. "http://localhost:9090".
func NewClient(baseURL, bearerToken string) *Client {
	transport := &bearerTransport{
		base: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
		token: bearerToken,
	}

	return &Client{
		rpc: sgpv1connect.NewSGPHarnessServiceClient(
			&http.Client{Transport: transport},
			baseURL,
		),
	}
}

// CreateSession synthesizes a session.start event and calls AppendEvent RPC.
func (c *Client) CreateSession(ctx context.Context, sess sgp.Session) error {
	event := sgp.Event{
		Kind:        sgp.EventKindSessionStart,
		Event:       sgp.DefaultEventNames().SessionStart,
		SessionID:   sess.ID,
		Timestamp:   sess.Timestamp,
		SpawnedFrom: sess.SpawnedFrom,
	}
	_, err := c.rpc.AppendEvent(ctx, connect.NewRequest(&sgpv1.AppendEventRequest{
		SessionId: string(sess.ID),
		Event:     convert.EventToProto(event),
	}))

	return err
}

// WriteNode synthesizes a node.appended or history.rewritten event and calls AppendEvent RPC.
func (c *Client) WriteNode(ctx context.Context, node sgp.Node) error {
	kind := sgp.EventKindNodeAppended
	eventName := sgp.DefaultEventNames().NodeAppended

	if len(node.SynthesizedFrom) > 0 {
		kind = sgp.EventKindHistoryRewritten
		eventName = sgp.DefaultEventNames().HistoryRewritten
	}

	n := node

	event := sgp.Event{
		Kind:      kind,
		Event:     eventName,
		SessionID: node.SessionID,
		Timestamp: node.Timestamp,
		Node:      &n,
	}

	_, err := c.rpc.AppendEvent(ctx, connect.NewRequest(&sgpv1.AppendEventRequest{
		SessionId: string(node.SessionID),
		Event:     convert.EventToProto(event),
	}))

	return err
}

// EndSession synthesizes a session.ended event and calls AppendEvent RPC.
func (c *Client) EndSession(ctx context.Context, sessionID sgp.ID, reason sgp.EndReason, terminalNodeID sgp.ID) error {
	event := sgp.Event{
		Kind:           sgp.EventKindSessionEnded,
		Event:          sgp.DefaultEventNames().SessionEnded,
		SessionID:      sessionID,
		Timestamp:      time.Now().UTC(),
		Reason:         reason,
		TerminalNodeID: terminalNodeID,
	}
	_, err := c.rpc.AppendEvent(ctx, connect.NewRequest(&sgpv1.AppendEventRequest{
		SessionId: string(sessionID),
		Event:     convert.EventToProto(event),
	}))

	return err
}

// LoadGraph calls LoadEvents RPC and restores the graph via RestoreFromEvents.
func (c *Client) LoadGraph(ctx context.Context, sessionID sgp.ID) (*sgp.Graph, error) {
	resp, err := c.rpc.LoadEvents(ctx, connect.NewRequest(&sgpv1.LoadEventsRequest{
		SessionId: string(sessionID),
	}))
	if err != nil {
		return nil, err
	}

	events := make([]sgp.Event, len(resp.Msg.GetEvents()))
	for i, e := range resp.Msg.GetEvents() {
		events[i] = convert.EventFromProto(e)
	}

	return sgp.RestoreFromEvents(events)
}

// GetNode returns ErrUnsupported (management client not wired).
func (c *Client) GetNode(_ context.Context, _ sgp.ID) (sgp.Node, error) {
	return sgp.Node{}, ErrUnsupported
}

// GetLineage returns ErrUnsupported (management client not wired).
func (c *Client) GetLineage(_ context.Context, _ sgp.ID) ([]sgp.Node, error) {
	return nil, ErrUnsupported
}

// GetSession returns ErrUnsupported (management client not wired).
func (c *Client) GetSession(_ context.Context, _ sgp.ID) (sgp.Session, sgp.SessionStatus, error) {
	return sgp.Session{}, 0, ErrUnsupported
}

// ListSessions returns ErrUnsupported (management client not wired).
func (c *Client) ListSessions(_ context.Context, _ string, _ int) ([]sgp.Session, string, error) {
	return nil, "", ErrUnsupported
}

// bearerTransport injects an Authorization header on every request.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)

	return t.base.RoundTrip(r)
}
