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

package cli

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/Lutfifakee-Project/GSEER/internal/version"
	"github.com/spf13/cobra"
)

// canonicalBannerLines is the Boss-specified canonical ASCII banner, split
// into lines.  Every character, backslash, forward slash, and whitespace
// cell must match exactly.
var canonicalBannerLines = []string{
	"   ___________ ________________ ",
	"  / ____/ ___// ____/ ____/ __ \\",
	" / / __ \\__ \\/ __/ / __/ / /_/ /",
	"/ /_/ /___/ / /___/ /___/ _, _/ ",
	`\____//____/_____/_____/_/ |_|`,
}

// TestBannerTemplateMatchesCanonical verifies that bannerTemplate is
// identical to Boss's canonical design: same line count, same characters,
// same backslashes, same forward slashes, same alignment and whitespace.
func TestBannerTemplateMatchesCanonical(t *testing.T) {
	// Strip the leading newline from the raw string template.
	got := strings.TrimPrefix(bannerTemplate, "\n")
	gotLines := strings.Split(got, "\n")

	// The last line contains the %s placeholder; remove it for comparison.
	lastIdx := len(gotLines) - 1
	gotLines[lastIdx] = strings.TrimSuffix(gotLines[lastIdx], "%s")

	if len(gotLines) != len(canonicalBannerLines) {
		t.Fatalf("bannerTemplate has %d lines, want %d\n--- got ---\n%s\n--- want ---\n%s",
			len(gotLines), len(canonicalBannerLines), got, strings.Join(canonicalBannerLines, "\n"))
	}

	for i := range canonicalBannerLines {
		if gotLines[i] != canonicalBannerLines[i] {
			t.Errorf("bannerTemplate line %d mismatch:\n  got: %q\n  want: %q", i+1, gotLines[i], canonicalBannerLines[i])
		}
	}
}

// TestBannerVersionDevFallback verifies that when Version is empty
// (no ldflags injection), bannerVersion returns "  vdev" (two spaces +
// fallback) and never a double prefix like "vvdev".
func TestBannerVersionDevFallback(t *testing.T) {
	orig := version.Version
	defer func() { version.Version = orig }()

	version.Version = ""
	if got := bannerVersion(); got != "  vdev" {
		t.Errorf("bannerVersion() = %q, want %q (fallback with two spaces)", got, "  vdev")
	}
	if strings.HasPrefix(bannerVersion(), "vv") {
		t.Errorf("bannerVersion() has double 'v' prefix: %q", bannerVersion())
	}
}

// TestBannerVersionV010 verifies that when Version is "v0.1.0" (as injected
// by ldflags), bannerVersion returns "  v0.1.0" (two spaces + version)
// without a double "vv" prefix.
func TestBannerVersionV010(t *testing.T) {
	orig := version.Version
	defer func() { version.Version = orig }()

	version.Version = "v0.1.0"
	if got := bannerVersion(); got != "  v0.1.0" {
		t.Errorf("bannerVersion() = %q, want %q", got, "  v0.1.0")
	}
	if strings.HasPrefix(bannerVersion(), "vv") {
		t.Errorf("bannerVersion() has double 'v' prefix: %q", bannerVersion())
	}
}

// TestBannerVersionNoDoublePrefix verifies that a version already carrying
// a "v" prefix is not double-prefixed.
func TestBannerVersionNoDoublePrefix(t *testing.T) {
	orig := version.Version
	defer func() { version.Version = orig }()

	version.Version = "v2.3.4"
	if got := bannerVersion(); got != "  v2.3.4" {
		t.Errorf("bannerVersion() = %q, want %q", got, "  v2.3.4")
	}
}

// TestBannerRenderedOutput verifies the full rendered banner contains the
// canonical lines and the version at the right edge of the last line with
// exactly two spaces before it.
func TestBannerRenderedOutput(t *testing.T) {
	orig := version.Version
	defer func() { version.Version = orig }()

	version.Version = "v0.1.0"

	buf := &bytes.Buffer{}
	if err := printBanner(buf); err != nil {
		t.Fatalf("printBanner returned error: %v", err)
	}
	out := buf.String()

	// Every canonical line must appear.
	for _, line := range canonicalBannerLines {
		if !strings.Contains(out, line) {
			t.Errorf("rendered banner missing canonical line %q\n--- output ---\n%s", line, out)
		}
	}

	// The last line must end with "|_|  v0.1.0" (two spaces before version).
	if !strings.Contains(out, `\____//____/_____/_____/_/ |_|  v0.1.0`) {
		t.Errorf("rendered banner missing final line with version:\n%s", out)
	}

	// No double "vv" prefix anywhere.
	if strings.Contains(out, "vv0") {
		t.Errorf("rendered banner contains double 'v' prefix:\n%s", out)
	}
}

// sentinelWriter is an io.Writer that always fails with sentinelErr.
type sentinelWriter struct{}

func (sentinelWriter) Write([]byte) (int, error) {
	return 0, sentinelErr
}

var sentinelErr = errors.New("sentinel write error")

// failingOnceWriter fails on the first Write with sentinelErr, then writes
// all subsequent bytes to the inner buffer.
type failingOnceWriter struct {
	inner *bytes.Buffer
	failed bool
}

func (w *failingOnceWriter) Write(p []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, sentinelErr
	}
	return w.inner.Write(p)
}

// TestPrintBannerReturnsWriterError verifies that printBanner propagates
// writer errors and that quiet mode suppresses output without error.
func TestPrintBannerReturnsWriterError(t *testing.T) {
	orig := flags.quiet
	defer func() { flags.quiet = orig }()

	// Non-quiet mode: writer error must propagate.
	flags.quiet = false
	if err := printBanner(sentinelWriter{}); err != sentinelErr {
		t.Errorf("printBanner() = %v, want %q", err, sentinelErr)
	}

	// Quiet mode: no output, no error.
	flags.quiet = true
	if err := printBanner(sentinelWriter{}); err != nil {
		t.Errorf("quiet mode printBanner should return nil, got: %v", err)
	}
}

// TestBannerHelpFuncReportsBannerWriteError verifies that bannerHelpFunc
// reports banner write errors to the command's error output while still
// rendering help.
func TestBannerHelpFuncReportsBannerWriteError(t *testing.T) {
	outBuf := &bytes.Buffer{}
	failingWriter := &failingOnceWriter{inner: outBuf}
	errBuf := &bytes.Buffer{}

	cmd := &cobra.Command{
		Use:   "test-cmd",
		Short: "test command",
	}
	cmd.SetOut(failingWriter)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{})
	defer cmd.SetArgs(nil)

	orig := flags.quiet
	defer func() { flags.quiet = orig }()
	flags.quiet = false

	// BannerHelpFunc must be set directly on cmd so that cmd.Help() invokes
	// it (cobra only walks up the parent chain when helpFunc is nil).
	cmd.SetHelpFunc(bannerHelpFunc)

	if err := cmd.Help(); err != nil {
		t.Errorf("cmd.Help() returned error: %v", err)
	}

	// Banner write error must be reported to stderr.
	if !strings.Contains(errBuf.String(), sentinelErr.Error()) {
		t.Errorf("stderr should contain sentinel error, got: %q", errBuf.String())
	}

	// Help text must still be rendered to stdout despite the banner write failure.
	if !strings.Contains(outBuf.String(), "test command") {
		t.Errorf("stdout should contain command help, got: %q", outBuf.String())
	}
}

// TestRootHelpShowsBannerOnce verifies that `gseer --help` shows exactly
// one banner (no duplication) at the top of the output.
func TestRootHelpShowsBannerOnce(t *testing.T) {
	buf := &bytes.Buffer{}
	origOut := RootCmd.OutOrStdout()
	origErr := RootCmd.ErrOrStderr()
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs([]string{"--help"})
	defer RootCmd.SetArgs([]string{})
	defer RootCmd.SetOut(origOut)
	defer RootCmd.SetErr(origErr)
	if err := RootCmd.Execute(); err != nil && !strings.Contains(err.Error(), flag.ErrHelp.Error()) {
		t.Fatalf("RootCmd --help returned unexpected error: %v", err)
	}
	help := buf.String()

	// Banner first line must appear.
	if !strings.Contains(help, "   ___________ ________________ ") {
		t.Errorf("help missing banner first line\n--- output ---\n%s", help)
	}

	// Count occurrences of the banner's last-line marker to detect duplicates.
	count := strings.Count(help, `\____//____/_____/_____/_/ |_|`)
	if count != 1 {
		t.Errorf("banner appears %d times in root --help, want exactly 1\n--- output ---\n%s", count, help)
	}
}

// TestSubcommandHelpNoBanner verifies that subcommand help (e.g.
// `gseer search --help`) does NOT print the ASCII banner.
func TestSubcommandHelpNoBanner(t *testing.T) {
	buf := &bytes.Buffer{}
	origOut := RootCmd.OutOrStdout()
	origErr := RootCmd.ErrOrStderr()
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs([]string{"search", "--help"})
	defer RootCmd.SetArgs([]string{})
	defer RootCmd.SetOut(origOut)
	defer RootCmd.SetErr(origErr)
	if err := RootCmd.Execute(); err != nil {
		t.Fatalf("RootCmd search --help returned error: %v", err)
	}
	out := buf.String()

	// Subcommand help must not contain the banner's first line.
	if strings.Contains(out, "   ___________ ________________ ") {
		t.Errorf("subcommand help should not contain ASCII banner\n--- output ---\n%s", out)
	}

	// But it should contain the command's own short description.
	if !strings.Contains(out, "Search the configured index with GQL") {
		t.Errorf("subcommand help missing its own description\n--- output ---\n%s", out)
	}
}

// TestQuietModeNoBanner verifies that quiet mode suppresses the banner.
func TestQuietModeNoBanner(t *testing.T) {
	orig := flags.quiet
	defer func() { flags.quiet = orig }()

	flags.quiet = true

	buf := &bytes.Buffer{}
	if err := printBanner(buf); err != nil {
		t.Fatalf("printBanner returned error: %v", err)
	}
	out := buf.String()

	if out != "" {
		t.Errorf("quiet mode should suppress banner, got: %q", out)
	}
}

// TestHelpCommandShowsBannerOnce verifies that `gseer help` shows exactly
// one canonical banner (no duplication).
func TestHelpCommandShowsBannerOnce(t *testing.T) {
	buf := &bytes.Buffer{}
	origOut := RootCmd.OutOrStdout()
	origErr := RootCmd.ErrOrStderr()
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs([]string{"help"})
	defer RootCmd.SetArgs([]string{})
	defer RootCmd.SetOut(origOut)
	defer RootCmd.SetErr(origErr)
	if err := RootCmd.Execute(); err != nil && !strings.Contains(err.Error(), flag.ErrHelp.Error()) {
		t.Fatalf("RootCmd help returned unexpected error: %v", err)
	}
	help := buf.String()

	if !strings.Contains(help, "   ___________ ________________ ") {
		t.Errorf("help missing banner first line\n--- output ---\n%s", help)
	}

	count := strings.Count(help, `\____//____/_____/_____/_/ |_|`)
	if count != 1 {
		t.Errorf("banner appears %d times in root help, want exactly 1\n--- output ---\n%s", count, help)
	}
}

// TestVersionCommandNoBanner verifies that `gseer version` does NOT show
// the ASCII banner, and that version metadata is still rendered correctly.
func TestVersionCommandNoBanner(t *testing.T) {
	buf := &bytes.Buffer{}
	origOut := RootCmd.OutOrStdout()
	origErr := RootCmd.ErrOrStderr()
	RootCmd.SetOut(buf)
	RootCmd.SetErr(buf)
	RootCmd.SetArgs([]string{"version"})
	defer RootCmd.SetArgs([]string{})
	defer RootCmd.SetOut(origOut)
	defer RootCmd.SetErr(origErr)
	if err := RootCmd.Execute(); err != nil {
		t.Fatalf("RootCmd version returned error: %v", err)
	}
	out := buf.String()

	// Version command must not contain the ASCII banner.
	if strings.Contains(out, "   ___________ ________________ ") {
		t.Errorf("version command should not contain ASCII banner\n--- output ---\n%s", out)
	}

	// Version metadata must still be present.
	if !strings.Contains(out, "gseer dev") {
		t.Errorf("version output missing 'gseer dev'\n--- output ---\n%s", out)
	}
	if !strings.Contains(out, "commit:") {
		t.Errorf("version output missing 'commit:'\n--- output ---\n%s", out)
	}
	if !strings.Contains(out, "built_at:") {
		t.Errorf("version output missing 'built_at:'\n--- output ---\n%s", out)
	}
	if !strings.Contains(out, "go:") {
		t.Errorf("version output missing 'go:'\n--- output ---\n%s", out)
	}
}
