// Package session models a lambda agent run end-to-end: identity,
// persistence, and the Workspace it currently points at. A Session
// outlives its Workspace (which rotates on /merge) and a Workspace can
// outlive its Session (suspend, abandon).
//
// This package owns the on-disk manifest at
// .lambda/sessions/<id>/session.json. Lockfile semantics, history
// persistence, and lifecycle verbs (resume, suspend, discard, merge)
// land in subsequent commits.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ManifestVersion is the schema version written to session.json. Bump on
// any change that older readers can't ignore.
const ManifestVersion = 1

// Manifest is the on-disk representation of a Session. Field tags match
// .scratch/sessions-redesign/01-decisions.md §8. The struct deliberately
// has no `status` field: existence of the manifest dir means the Session
// exists; the lockfile (added in a later step) decides active vs suspended.
type Manifest struct {
	ID            string    `json:"id"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	LastActiveAt  time.Time `json:"last_active_at"`
	Title         *string   `json:"title"`
	WorkspaceID   string    `json:"workspace_id"`
	BaseBranch    string    `json:"base_branch"`
	BaseStartSHA  string    `json:"base_start_sha"`
	Model         string    `json:"model"`
	Provider      string    `json:"provider"`
}

// NewID returns a fresh Session id of the form
// `lambda-YYYYMMDD-HHMMSS-xxxx`. The 4-char hex suffix is unconditional
// because oneshot subagent loops can spawn many Sessions per second; a
// guaranteed tiebreaker is cheaper than a collision-detection branch.
func NewID() string {
	ts := time.Now().UTC().Format("20060102-150405")
	var b [2]byte
	_, _ = rand.Read(b[:])
	return "lambda-" + ts + "-" + hex.EncodeToString(b[:])
}

// SessionsDir returns <repoRoot>/.lambda/sessions/.
func SessionsDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".lambda", "sessions")
}

// ManifestPath returns the path to session.json for the given id under
// repoRoot. Empty repoRoot or id yields an empty path.
func ManifestPath(repoRoot, id string) string {
	if repoRoot == "" || id == "" {
		return ""
	}
	return filepath.Join(SessionsDir(repoRoot), id, "session.json")
}

// ErrManifestNotFound is returned by Load when the manifest file does
// not exist. Callers can treat this as "no such Session" without having
// to check os.IsNotExist on the wrapped error.
var ErrManifestNotFound = errors.New("session manifest not found")

// Save writes the manifest atomically: write to a sibling temp file,
// fsync, rename. The session directory is created if missing.
func (m *Manifest) Save(repoRoot string) error {
	if m.ID == "" {
		return errors.New("manifest: empty id")
	}
	dir := filepath.Join(SessionsDir(repoRoot), m.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("session: mkdir %s: %w", dir, err)
	}
	final := filepath.Join(dir, "session.json")
	tmp, err := os.CreateTemp(dir, "session.json.*.tmp")
	if err != nil {
		return fmt.Errorf("session: create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below fails before the rename.
	defer func() { _ = os.Remove(tmpPath) }()

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: encode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("session: close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, final); err != nil {
		return fmt.Errorf("session: rename %s: %w", final, err)
	}
	return nil
}

// Load reads the manifest for the given id under repoRoot. Returns
// ErrManifestNotFound if the file is missing.
func Load(repoRoot, id string) (*Manifest, error) {
	path := ManifestPath(repoRoot, id)
	if path == "" {
		return nil, errors.New("session: empty repoRoot or id")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrManifestNotFound
		}
		return nil, fmt.Errorf("session: read %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("session: parse %s: %w", path, err)
	}
	return &m, nil
}

// List returns the manifests of every Session under repoRoot, in no
// guaranteed order. Subdirs without a session.json (or with a malformed
// one) are silently skipped — they're either in-progress writes or
// foreign data we shouldn't crash on.
func List(repoRoot string) ([]*Manifest, error) {
	root := SessionsDir(repoRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: read %s: %w", root, err)
	}
	out := make([]*Manifest, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Filter to ids that look like ours so a stray dir doesn't pollute
		// the list. We don't validate the random suffix because future
		// formats may extend it.
		if !strings.HasPrefix(e.Name(), "lambda-") {
			continue
		}
		m, err := Load(repoRoot, e.Name())
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}
