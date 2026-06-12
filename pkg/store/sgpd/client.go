// Package sgpd provides a gRPC client store backed by the sgpd service.
package sgpd

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	sgpv1 "github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1"
	"github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1/sgpv1connect"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/store/sgpd/convert"
)

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

// AppendEvent sends a single event to the sgpd server.
func (c *Client) AppendEvent(ctx context.Context, sessionID sgp.ID, event sgp.Event) error {
	_, err := c.rpc.AppendEvent(ctx, connect.NewRequest(&sgpv1.AppendEventRequest{
		SessionId: string(sessionID),
		Event:     convert.EventToProto(event),
	}))

	return err
}

// LoadEvents fetches the full event log from the sgpd server and restores Kind.
func (c *Client) LoadEvents(ctx context.Context, sessionID sgp.ID) ([]sgp.Event, error) {
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

	return events, nil
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
