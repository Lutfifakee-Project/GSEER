// GSEER — Internet Asset Intelligence & Search Engine.
// Copyright (C) 2026  The GSEER Authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// apply.go — download + verify + atomic replace + rollback.

// Error is the update system's structured error (what → why → next).
type Error struct {
	Code string
	What string
	Why  string
	Next string
}

func (e *Error) Error() string {
	out := e.Code + ": " + e.What
	if e.Why != "" {
		out += "\nWhy: " + e.Why
	}
	if e.Next != "" {
		out += "\nTry: " + e.Next
	}
	return out
}

// MaxArtifactBytes caps an update payload (a GSEER binary is tens of
// MB; 512 MiB is generous and refuses memory-bomb URLs).
const MaxArtifactBytes = 512 << 20

// Fetcher downloads a URL with a hard cap and timeout.  It refuses
// non-HTTP(S) schemes and redacts userinfo-bearing URLs in errors.
type Fetcher struct {
	Client *http.Client
	// Now allows tests to pin time (unused today; reserved).
}

// Fetch reads the whole body (≤ MaxArtifactBytes).
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, &Error{
			Code: "E_UPDATE_URL",
			What: "the artifact URL is not HTTP(S)",
			Why:  redactURL(rawURL),
			Next: "the manifest is broken; fetch the official one",
		}
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &Error{Code: "E_UPDATE_URL", What: "cannot build the request", Why: redactURL(rawURL)}
	}
	req.Header.Set("User-Agent", "gseer-updater/1.0 (signed-update protocol v1)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, &Error{
			Code: "E_UPDATE_UNREACHABLE",
			What: "the artifact URL did not answer",
			Why:  err.Error() + " (" + redactURL(rawURL) + ")",
			Next: "check network connectivity; retry later",
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{
			Code: "E_UPDATE_HTTP",
			What: fmt.Sprintf("the artifact URL returned HTTP %d", resp.StatusCode),
			Why:  redactURL(rawURL),
			Next: "the release channel may be down or the URL stale",
		}
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, MaxArtifactBytes+1))
	if err != nil {
		return nil, &Error{Code: "E_UPDATE_READ", What: "reading the artifact failed", Why: err.Error()}
	}
	if len(payload) > MaxArtifactBytes {
		return nil, &Error{Code: "E_UPDATE_SIZE", What: "the artifact exceeds the size cap", Next: "refuse; a legitimate release fits the cap"}
	}
	return payload, nil
}

// redactURL strips userinfo (credentials never surface in errors).
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable URL)"
	}
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	return u.String()
}

// ApplyResult reports a completed replacement.
type ApplyResult struct {
	PreviousVersion string
	NewVersion      string
	Replaced        string
	Backup          string // "" when the replace had no prior file
	Bytes           int64
}

// Apply runs the full chain: verify → version check → platform select →
// fetch → checksum → atomic replace (+ backup, + rollback on failure).
// currentVersion is the running binary's version string.
func Apply(ctx context.Context, manifestBytes []byte, pub []byte, currentVersion, targetPath string, force bool, fetcher *Fetcher) (*ApplyResult, error) {
	// 1. Signature FIRST — before anything in the manifest is believed.
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	if err := manifest.VerifySignature(ed25519Key(pub)); err != nil {
		return nil, err
	}

	// 2. Version gate (downgrade/same-version refused without force).
	cmp, err := CompareVersions(currentVersion, manifest.Version)
	if err != nil {
		return nil, &Error{
			Code: "E_UPDATE_VERSION",
			What: "cannot compare versions",
			Why:  err.Error(),
			Next: "refusing to apply an update of unknown relation to the running build",
		}
	}
	if cmp >= 0 && !force {
		return nil, &Error{
			Code: "E_UPDATE_NOT_NEWER",
			What: fmt.Sprintf("manifest version %s is not newer than the running %s", manifest.Version, currentVersion),
			Why:  "downgrades and same-version re-writes are refused without --force",
			Next: "if you really want to re-install, pass --force",
		}
	}

	// 3. Platform selection — exact GOOS/GOARCH, no guessing.
	art, err := manifest.SelectArtifact(goos, goarch)
	if err != nil {
		return nil, err
	}

	// 4. Fetch + checksum.
	payload, err := fetcher.Fetch(ctx, art.URL)
	if err != nil {
		return nil, err
	}
	if err := art.VerifyChecksum(payload); err != nil {
		return nil, err
	}

	// 5. Atomic replace with backup + rollback.
	if err := replaceBinary(targetPath, payload); err != nil {
		return nil, err
	}
	return &ApplyResult{
		PreviousVersion: currentVersion,
		NewVersion:      manifest.Version,
		Replaced:        targetPath,
		Backup:          targetPath + ".bak",
		Bytes:           int64(len(payload)),
	}, nil
}

// replaceBinary writes payload to a temp file next to target (same
// filesystem → os.Rename is atomic), fsyncs, preserves the mode,
// renames over the target, and keeps the previous file as target.bak.
// Any failure rolls the target back from the backup and removes temps.
func replaceBinary(target string, payload []byte) (err error) {
	dir := filepath.Dir(target)
	if st, serr := os.Stat(target); serr == nil && st.IsDir() {
		return &Error{Code: "E_UPDATE_TARGET", What: "the update target is a directory", Why: target}
	}
	if derr := os.MkdirAll(dir, 0o755); derr != nil {
		return &Error{Code: "E_UPDATE_TARGET", What: "cannot create the target directory", Why: derr.Error()}
	}

	mode := os.FileMode(0o755)
	if st, serr := os.Stat(target); serr == nil {
		mode = st.Mode().Perm()
	}

	backup := target + ".bak"
	tmp, terr := os.CreateTemp(dir, ".gseer-update-*")
	if terr != nil {
		return &Error{Code: "E_UPDATE_TEMP", What: "cannot create the temp file next to the target", Why: terr.Error()}
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort temp cleanup on every exit path.
		_ = os.Remove(tmpName)
	}()

	if _, werr := tmp.Write(payload); werr != nil {
		tmp.Close()
		return &Error{Code: "E_UPDATE_TEMP", What: "writing the payload failed", Why: werr.Error()}
	}
	if serr := tmp.Sync(); serr != nil {
		tmp.Close()
		return &Error{Code: "E_UPDATE_TEMP", What: "fsync failed", Why: serr.Error()}
	}
	if cerr := tmp.Close(); cerr != nil {
		return &Error{Code: "E_UPDATE_TEMP", What: "closing the temp file failed", Why: cerr.Error()}
	}
	if perr := os.Chmod(tmpName, mode); perr != nil {
		return &Error{Code: "E_UPDATE_TEMP", What: "cannot set the binary mode", Why: perr.Error()}
	}

	// Keep the running binary for rollback.
	hadBackup := false
	if _, serr := os.Stat(target); serr == nil {
		// Copy the CURRENT file to .bak (rename would move it away while
		// the running process maps it — copying is safe everywhere).
		if cerr := copyFile(target, backup); cerr != nil {
			return &Error{Code: "E_UPDATE_BACKUP", What: "cannot back up the current binary", Why: cerr.Error()}
		}
		hadBackup = true
	}

	if rerr := os.Rename(tmpName, target); rerr != nil {
		// Rollback: restore the backup over the (possibly clobbered)
		// target.  On most systems rename either succeeds or leaves the
		// target untouched, but the recovery path is unconditional.
		if hadBackup {
			if rerr2 := copyFile(backup, target); rerr2 != nil {
				return errors.Join(
					&Error{Code: "E_UPDATE_REPLACE", What: "rename failed AND rollback failed", Why: rerr.Error() + "; rollback: " + rerr2.Error(), Next: "restore " + backup + " manually"},
					rerr)
			}
		}
		return &Error{Code: "E_UPDATE_REPLACE", What: "atomic rename failed", Why: rerr.Error()}
	}
	// Re-sync the directory entry so the rename survives a crash.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// Check runs the verification chain WITHOUT downloading: signature,
// version relation, and platform availability.
func Check(manifestBytes []byte, pub []byte, currentVersion string) (*Manifest, error) {
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	if err := manifest.VerifySignature(ed25519Key(pub)); err != nil {
		return nil, err
	}
	if _, err := manifest.SelectArtifact(goos, goarch); err != nil {
		return nil, err
	}
	cmp, err := CompareVersions(currentVersion, manifest.Version)
	if err != nil {
		return nil, &Error{Code: "E_UPDATE_VERSION", What: "cannot compare versions", Why: err.Error()}
	}
	if cmp >= 0 {
		return manifest, &Error{
			Code: "E_UPDATE_UP_TO_DATE",
			What: fmt.Sprintf("the running %s is not older than the manifest %s", currentVersion, manifest.Version),
			Next: "nothing to do",
		}
	}
	return manifest, nil
}

// IsUpToDate reports whether err is the "not older" condition (which is
// a successful check, not a failure).
func IsUpToDate(err error) bool {
	var ue *Error
	if errors.As(err, &ue) {
		return ue.Code == "E_UPDATE_UP_TO_DATE"
	}
	return false
}

// LoadManifest reads manifest bytes from a path or fetches a URL.
func LoadManifest(ctx context.Context, location string, fetcher *Fetcher) ([]byte, error) {
	if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		return fetcher.Fetch(ctx, location)
	}
	data, err := os.ReadFile(location)
	if err != nil {
		return nil, &Error{Code: "E_UPDATE_MANIFEST_IO", What: "cannot read the manifest file", Why: err.Error()}
	}
	return data, nil
}
