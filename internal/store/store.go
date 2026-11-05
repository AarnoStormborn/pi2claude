// Package store handles pi2claude's manifest, atomic writes, and provenance.
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ManifestEntry records one migrated session.
type ManifestEntry struct {
	PiPath           string `json:"piPath"`
	PiSize           int64  `json:"piSize"`
	PiMTime          int64  `json:"piMTime"`
	PiSHA256         string `json:"piSHA256"`
	ClaudeSessionID  string `json:"claudeSessionId"`
	ClaudePath       string `json:"claudePath"`
	ConvertedAt      string `json:"convertedAt"`
	ConverterVersion string `json:"converterVersion"`
}

// Manifest is the on-disk migration index.
type Manifest struct {
	Version  int                      `json:"version"`
	Sessions map[string]ManifestEntry `json:"sessions"`
}

// ManifestPath returns <claudeDir>/pi2claude/manifest.json.
func ManifestPath(claudeDir string) string {
	return filepath.Join(claudeDir, "pi2claude", "manifest.json")
}

// LoadManifest loads a manifest, returning an empty one when absent.
func LoadManifest(path string) (*Manifest, error) {
	m := &Manifest{Version: 1, Sessions: make(map[string]ManifestEntry)}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.Sessions == nil {
		m.Sessions = make(map[string]ManifestEntry)
	}
	if m.Version == 0 {
		m.Version = 1
	}
	return m, nil
}

// Save writes the manifest atomically.
func SaveManifest(path string, m *Manifest) error {
	if m.Sessions == nil {
		m.Sessions = make(map[string]ManifestEntry)
	}
	m.Version = 1
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return AtomicWrite(path, b, 0o600)
}

// AtomicWrite writes data to a temporary file in the same directory and renames
// it into place.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pi2claude-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// Backup copies path to path.bak.<unix-nanos>.
func Backup(path string) (string, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer in.Close()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	dst := path + ".bak." + stamp
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return "", err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return "", err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return dst, nil
}

// HasMarker reports whether path starts with a pi2claude provenance record for
// piSessionID.
func HasMarker(path, piSessionID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if !sc.Scan() {
		return false
	}
	var row struct {
		Type        string `json:"type"`
		PiSessionID string `json:"piSessionId"`
	}
	if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
		return false
	}
	return row.Type == "pi-migration" && row.PiSessionID == piSessionID
}

// SameFile reports whether two paths refer to the same existing file.
func SameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}
