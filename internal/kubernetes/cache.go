package kubernetes

import (
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// CacheTTL is the discovery freshness window (spec §9): 5 minutes,
// matching the in-memory caches for cluster lists and metrics discovery.
const CacheTTL = 5 * time.Minute

// DefaultCacheDir is where persisted discovery lives (spec §9). The disk
// cache is not an optional optimization: clients that spawn the server per
// command (e.g. srectl) would otherwise pay 1–2s of discovery on every
// call.
func DefaultCacheDir() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "mcp-k8s")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "mcp-k8s")
}

// unsafePathChars matches anything that must not appear in a cache
// directory name derived from a cluster name.
var unsafePathChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// clusterCacheDir returns the per-cluster cache directory, sanitizing the
// cluster name so it cannot escape the base directory (spec §9: cache is
// per cluster).
func clusterCacheDir(base, cluster string) string {
	safe := unsafePathChars.ReplaceAllString(cluster, "_")
	return filepath.Join(base, "discovery", safe)
}

// ensurePrivateDir creates dir (and parents) with 0700 permissions
// (spec §9: cache directory permissions). Existing directories are
// chmodded if they are too permissive.
func ensurePrivateDir(dir string) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if !info.IsDir() {
			return &os.PathError{Op: "mkdir", Path: dir, Err: os.ErrExist}
		}
		if info.Mode().Perm() != 0o700 {
			if err := os.Chmod(dir, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}
