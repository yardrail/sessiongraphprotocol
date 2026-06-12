package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
)

// Observation is the internal live-event type delivered to WatchSession subscribers.
type Observation struct {
	Seq       int64
	Event     sgp.Event
	HeadID    sgp.ID
	Status    SessionStatus
	EndReason sgp.EndReason
	NodeCount int32
}

// SessionStatus mirrors the proto enum for internal use.
type SessionStatus int

const (
	// SessionStatusOpen indicates the session is active and accepting new nodes.
	SessionStatusOpen SessionStatus = 1
	// SessionStatusClosed indicates the session has ended.
	SessionStatusClosed SessionStatus = 2
)

type subscriber struct {
	ch chan Observation
}

// NotifyBroker uses a dedicated pgx connection to LISTEN for per-session
// notifications and fan them out to in-process subscribers.
type NotifyBroker struct {
	conn     *pgx.Conn
	pool     *pgxpool.Pool
	mu       sync.RWMutex
	subs     map[string][]subscriber
	listened map[string]struct{}
}

// NewNotifyBroker creates a broker using a dedicated (non-pooled) connection
// for LISTEN. The pool is used only to fetch event rows when a notification arrives.
func NewNotifyBroker(
	ctx context.Context,
	databaseURL string,
	pool *pgxpool.Pool,
) (*NotifyBroker, error) {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("notify broker connect: %w", err)
	}

	return &NotifyBroker{
		conn:     conn,
		pool:     pool,
		subs:     make(map[string][]subscriber),
		listened: make(map[string]struct{}),
	}, nil
}

// Subscribe registers a subscriber for sessionID and returns a channel that
// receives Observations, plus a cancel func to unsubscribe.
func (b *NotifyBroker) Subscribe(
	_ context.Context,
	sessionID string,
) (<-chan Observation, func()) {
	const subscriberBufSize = 64

	ch := make(chan Observation, subscriberBufSize)
	sub := subscriber{ch: ch}

	b.mu.Lock()
	b.subs[sessionID] = append(b.subs[sessionID], sub)
	needListen := false

	if _, ok := b.listened[sessionID]; !ok {
		b.listened[sessionID] = struct{}{}
		needListen = true
	}
	b.mu.Unlock()

	if needListen {
		channel := pgx.Identifier{"sgp:" + sessionID}.Sanitize()
		// LISTEN channel names cannot be parameterised; sanitize guards against injection.
		b.conn.Exec(context.Background(), "LISTEN "+channel) //nolint:errcheck,contextcheck
	}

	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()

		subs := b.subs[sessionID]
		for i, s := range subs {
			if s.ch == ch {
				b.subs[sessionID] = append(subs[:i], subs[i+1:]...)

				break
			}
		}

		close(ch)
	}

	return ch, cancel
}

// Run blocks, dispatching notifications until ctx is cancelled.
func (b *NotifyBroker) Run(ctx context.Context) error {
	for {
		notification, err := b.conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() == nil {
				return fmt.Errorf("notify broker wait: %w", err)
			}

			return nil
		}

		b.handleNotification(ctx, notification.Channel, notification.Payload)
	}
}

// Close closes the dedicated connection.
func (b *NotifyBroker) Close(ctx context.Context) error {
	return b.conn.Close(ctx)
}

func (b *NotifyBroker) handleNotification(ctx context.Context, channel, payload string) {
	sessionID := strings.TrimPrefix(channel, "sgp:")

	seq, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return
	}

	obs, err := b.fetchObservation(ctx, sessionID, seq)
	if err != nil {
		return
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, sub := range b.subs[sessionID] {
		select {
		case sub.ch <- obs:
		default: // subscriber too slow; drop
		}
	}
}

func (b *NotifyBroker) fetchObservation(
	ctx context.Context,
	sessionID string,
	seq int64,
) (Observation, error) {
	var eventJSON []byte

	err := b.pool.QueryRow(ctx,
		`SELECT event_json FROM sgp_events WHERE session_id = $1 AND seq = $2`,
		sessionID, seq).Scan(&eventJSON)
	if err != nil {
		return Observation{}, fmt.Errorf("fetch observation event: %w", err)
	}

	var event sgp.Event

	err = json.Unmarshal(eventJSON, &event)
	if err != nil {
		return Observation{}, fmt.Errorf("unmarshal observation event: %w", err)
	}

	event.Kind = sgp.ClassifyEvent(event)

	obs := Observation{
		Seq:   seq,
		Event: event,
	}

	if event.Node != nil {
		obs.HeadID = event.Node.ID
	} else if event.TerminalNodeID != "" {
		obs.HeadID = event.TerminalNodeID
	}

	if event.Kind == sgp.EventKindSessionEnded {
		obs.Status = SessionStatusClosed
		obs.EndReason = event.Reason
	} else {
		obs.Status = SessionStatusOpen
	}

	var count int32
	b.pool.QueryRow(ctx,
		`SELECT count(*) FROM sgp_events WHERE session_id = $1 AND event_json ? 'node'`,
		sessionID).Scan(&count) //nolint:errcheck
	obs.NodeCount = count

	return obs, nil
}
