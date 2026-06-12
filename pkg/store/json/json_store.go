// Package jsonstore persists SGP session event logs as append-only JSONL files on local disk.
package jsonstore //nolint:revive // package name differs from directory intentionally

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
)

// JSONFileStore persists one JSONL event log per session on local disk.
// Each event is written as a single JSON object on its own line. Events are
// appended in emission order and never modified after writing.
//
// JSONFileStore is not safe for concurrent AppendEvent calls targeting the
// same session. Callers must serialise writes per session.
type JSONFileStore struct {
	baseDir string
}

const (
	dirPerm  = 0o755
	filePerm = 0o644
)

var (
	_ sgp.Store = (*JSONFileStore)(nil)

	errBaseDirRequired = errors.New("base dir is required")
)

// NewJSONFileStore creates a store rooted at baseDir.
func NewJSONFileStore(baseDir string) (*JSONFileStore, error) {
	if strings.TrimSpace(baseDir) == "" {
		return nil, errBaseDirRequired
	}

	return &JSONFileStore{baseDir: baseDir}, nil
}

// AppendEvent marshals event as a single JSON line and appends it to the
// session's event log file. The file is created on the first append.
func (store *JSONFileStore) AppendEvent(
	ctx context.Context,
	sessionID sgp.ID,
	event sgp.Event,
) error {
	err := ctx.Err()
	if err != nil {
		return err
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	err = os.MkdirAll(store.baseDir, dirPerm)
	if err != nil {
		return fmt.Errorf("create base dir: %w", err)
	}

	f, err := os.OpenFile(
		store.pathForSession(sessionID),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		filePerm,
	)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s\n", data)
	if err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	return nil
}

// LoadEvents reads all events for sessionID from the JSONL file in emission
// order. Returns [sgp.ErrGraphNotFound] if no events have been recorded for
// the session. The Kind field is restored on each event using
// [sgp.ClassifyEvent].
func (store *JSONFileStore) LoadEvents(ctx context.Context, sessionID sgp.ID) ([]sgp.Event, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}

	f, err := os.Open(store.pathForSession(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", sgp.ErrGraphNotFound, sessionID)
		}

		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	var events []sgp.Event

	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event sgp.Event

		err = json.Unmarshal(line, &event)
		if err != nil {
			return nil, fmt.Errorf("parse event at line %d: %w", lineNum, err)
		}

		event.Kind = sgp.ClassifyEvent(event)
		events = append(events, event)
	}

	err = scanner.Err()
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	return events, nil
}

func (store *JSONFileStore) pathForSession(sessionID sgp.ID) string {
	encoded := url.PathEscape(string(sessionID))

	return filepath.Join(store.baseDir, encoded+".jsonl")
}
