package cayleystore

import (
	"context"
	"sync"

	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
)

const watchBufSize = 64

// watcherMixin is an embeddable in-process pub/sub mixin for Store.
// It satisfies sgp.Watcher when embedded in a Store that calls publish
// after successful WriteNode calls.
type watcherMixin struct {
	mu   sync.RWMutex
	subs map[sgp.ID][]chan sgp.Node
}

func (w *watcherMixin) init() {
	w.subs = make(map[sgp.ID][]chan sgp.Node)
}

// publish sends node to all subscribers for sessionID. Slow subscribers are dropped.
func (w *watcherMixin) publish(sessionID sgp.ID, node sgp.Node) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	for _, ch := range w.subs[sessionID] {
		select {
		case ch <- node:
		default: // subscriber too slow; drop
		}
	}
}

// Watch subscribes to live node writes for sessionID.
// The returned cancel func unsubscribes and closes the channel.
func (w *watcherMixin) Watch(ctx context.Context, sessionID sgp.ID) (<-chan sgp.Node, func(), error) {
	ch := make(chan sgp.Node, watchBufSize)

	w.mu.Lock()
	w.subs[sessionID] = append(w.subs[sessionID], ch)
	w.mu.Unlock()

	cancel := func() {
		w.mu.Lock()
		defer w.mu.Unlock()

		subs := w.subs[sessionID]
		for i, sub := range subs {
			if sub == ch {
				w.subs[sessionID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		close(ch)
	}

	return ch, cancel, nil
}
