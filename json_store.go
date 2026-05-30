package sessiongraphprotocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// JSONFileStore persists one graph snapshot per JSON file on local disk.
type JSONFileStore struct {
	baseDir string
}

var _ Store = (*JSONFileStore)(nil)

// NewJSONFileStore creates a store rooted at baseDir.
func NewJSONFileStore(baseDir string) (*JSONFileStore, error) {
	if strings.TrimSpace(baseDir) == "" {
		return nil, errors.New("base dir is required")
	}

	return &JSONFileStore{baseDir: baseDir}, nil
}

// Save writes a graph snapshot to local disk.
func (store *JSONFileStore) Save(ctx context.Context, graph *Graph) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if graph == nil {
		return ErrNilGraph
	}

	snapshot := graph.Snapshot()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	if err = os.MkdirAll(store.baseDir, 0o755); err != nil {
		return fmt.Errorf("create base dir: %w", err)
	}

	filePath := store.pathForSession(snapshot.Session.ID)
	tempFile, err := os.CreateTemp(store.baseDir, ".sgp-*.json")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	tempName := tempFile.Name()
	defer func() {
		_ = os.Remove(tempName)
	}()

	if _, err = tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("write temp file: %w", err)
	}

	if err = tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err = ctx.Err(); err != nil {
		return err
	}

	if err = os.Rename(tempName, filePath); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// Load reads a graph snapshot from local disk and restores it.
func (store *JSONFileStore) Load(ctx context.Context, sessionID ID) (*Graph, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(store.pathForSession(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrGraphNotFound, sessionID)
		}

		return nil, fmt.Errorf("read graph snapshot: %w", err)
	}

	var snapshot GraphSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal graph snapshot: %w", err)
	}

	graph, err := RestoreGraph(snapshot)
	if err != nil {
		return nil, err
	}

	return graph, nil
}

func (store *JSONFileStore) pathForSession(sessionID ID) string {
	encoded := url.PathEscape(string(sessionID))

	return filepath.Join(store.baseDir, encoded+".json")
}