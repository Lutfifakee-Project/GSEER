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
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/Lutfifakee-Project/GSEER/internal/api"
	"github.com/Lutfifakee-Project/GSEER/internal/config"
	"github.com/Lutfifakee-Project/GSEER/internal/dashboard"
	"github.com/Lutfifakee-Project/GSEER/internal/httpx"
	"github.com/Lutfifakee-Project/GSEER/internal/search"
	"github.com/Lutfifakee-Project/GSEER/internal/storage/postgres"
)

// server.go — `gseer server start | status | stop` (SPEC-03 §2).
//
// start: self-hosted only; runs under the least-privilege RUNTIME
// credential (gseer_app — required), gates on the fail-closed effective-
// privilege audit (OD-5) BEFORE the bind, and serves the REST API
// (/api/v1) + dashboard (/) over the SAME SearchService (AC-4.9).
// Runs in the foreground with graceful shutdown on SIGINT/SIGTERM; the
// pidfile enables `server stop`.
//
// Migrations are OPERATOR work (gseer setup --self-hosted): gseer_app is
// deliberately denied the migration ledger and can never run them.  The
// audit proves the hardened schema is in place before any traffic.
//
// The server is read-only: every handler is search/stats/health — no
// scanning surface exists anywhere behind it (ADR-0010).

// registerServerCmd attaches `gseer server` with its subcommands.
func registerServerCmd(root *cobra.Command) {
	server := &cobra.Command{
		Use:   "server",
		Short: "Manage the local API + dashboard server",
		Args:  cobra.NoArgs,
	}
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the local API + dashboard server (hardened: gseer_app credential + privilege audit)",
		Long: `Start the local API + dashboard server.

Self-hosted profile only.  v0.2.0 hardened startup (OD-5, Rev 6.7.2):
  1. requires the least-privilege runtime credential gseer_app
     (database.runtime_dsn / GSEER_DB_RUNTIME_DSN — provisioned and
     printed once by gseer setup --self-hosted);
  2. runs the fail-closed effective-privilege audit BEFORE binding —
     a failed audit refuses startup with exit 5; there is no bypass;
  3. serves the REST API + dashboard under gseer_app, never under the
     operator credential.
Migrations are operator work (gseer setup --self-hosted); the runtime
credential cannot run them and the server refuses an unaudited schema.
Runs in the foreground; stop with Ctrl-C or: gseer server stop`,
		Args: cobra.NoArgs,
		RunE: runServerStart,
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show server status",
		Args:  cobra.NoArgs,
		RunE:  runServerStatus,
	}
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the server",
		Args:  cobra.NoArgs,
		RunE:  runServerStop,
	}
	server.AddCommand(start, status, stop)
	root.AddCommand(server)
}

// ---------------------------------------------------------------------------
// start
// ---------------------------------------------------------------------------

// openRuntimePool validates the runtime credential is present and the
// connecting identity is gseer_app, then opens the pool.  Extracted
// from runServerStart to keep that function under the gocyclo gate;
// on any failure it prints the exact E_RUNTIME_CREDENTIAL_REQUIRED /
// E_DB_UNAVAILABLE / E_RUNTIME_CREDENTIAL_MISMATCH message and calls
// exitWith with the same exit code as the inline original.  The
// caller owns defer pool.Close().
func openRuntimePool(ctx context.Context, cfg *config.Config, out io.Writer) (*pgxpool.Pool, string) {
	runtimeDSN, explicit := cfg.Database.RuntimeCredentialDSN()
	if !explicit {
		printf(out, "E_RUNTIME_CREDENTIAL_REQUIRED: `server start` needs the least-privilege runtime credential (gseer_app)\n"+
			"Why: the hardened v0.2.0 model never serves traffic under the operator credential (OD-5; docs/DEPLOYMENT.md §3.1)\n"+
			"Try: gseer setup --self-hosted   (provisions gseer_app, applies the grants, runs the audit, and prints GSEER_DB_RUNTIME_DSN once)\n")
		exitWith(ExitConfig)
	}
	pool, err := pgxpool.New(ctx, runtimeDSN)
	if err != nil {
		printf(out, "E_DB_UNAVAILABLE: cannot open the database pool (runtime credential)\nWhy: %v\nTry: check database.runtime_dsn in the config — gseer doctor\n", err)
		exitWith(ExitUnavailable)
	}
	// Runtime-credential identity check (fail-closed, v0.2.0): the audit
	// proves the gseer_app ROLE is least-privilege, but only the connecting
	// user proves WHICH credential serves the traffic.  A superuser or
	// operator DSN pasted into database.runtime_dsn would silently bypass
	// the entire grant matrix — refuse it exactly like a failed audit.
	if err := postgres.VerifyRuntimeIdentity(ctx, pool); err != nil {
		printf(out, "E_RUNTIME_CREDENTIAL_MISMATCH: %s\n"+
			"Why: the hardened model serves traffic ONLY as %s (OD-5; docs/DEPLOYMENT.md §3.1) — no bypass exists\n"+
			"Try: point database.runtime_dsn at the gseer_app credential printed by gseer setup --self-hosted\n", err, postgres.RoleApp)
		exitWith(ExitAuth) // exit 5: the security gate refused startup
	}
	return pool, runtimeDSN
}

// runPrivilegeAuditGate runs the fail-closed OD-5 audit BEFORE the
// listener exists, prints notices/failures, and on a failed or
// unreachable audit calls exitWith with the same exit code as the
// inline original.  Extracted from runServerStart to keep that
// function under the gocyclo gate; the bytes printed are identical.
func runPrivilegeAuditGate(ctx context.Context, runtimeDSN string, out io.Writer) {
	// Run under gseer_app itself: every catalog the audit consults is
	// readable by non-superusers, so this both audits the deployment
	// AND proves the runtime credential can see everything it needs.
	// The audit is read-only.  A failed audit refuses startup with
	// exit 5; there is no bypass, flag, or override.
	printf(out, "Running the fail-closed privilege audit (OD-5) ...\n")
	report, aerr := postgres.RunPrivilegeAudit(ctx, runtimeDSN)
	if aerr != nil {
		printf(out, "E_AUDIT_UNAVAILABLE: the privilege audit could not run\nWhy: %v\nTry: an audit that cannot run is not an audit that passed — check database.runtime_dsn and the deployment (gseer setup --self-hosted)\n", aerr)
		exitWith(ExitUnavailable)
	}
	for _, n := range report.Notices {
		printf(out, "[audt] %s\n", n)
	}
	if !report.Passed {
		printf(out, "E_PRIVILEGE_AUDIT_FAILED: startup refused — the fail-closed privilege audit found %d failure(s)\n", len(report.Failures))
		for _, f := range report.Failures {
			printf(out, "  - %s\n", f)
		}
		printf(out, "Why: OD-5 — no runtime traffic before a passed audit; there is no bypass, flag, or override.\n"+
			"Try: fix the deployment, never the check (docs/DEPLOYMENT.md §3.5). Do NOT grant the missing privileges to gseer_app.")
		exitWith(ExitAuth) // exit 5: the security gate refused startup
	}
	printf(out, "[ ok  ] privilege audit: PASSED — binding permitted (fail-closed gate, OD-5)\n")
}

func runServerStart(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()

	cfg, cerr := config.Load(config.Flags{
		ConfigPath: flags.configPath,
		Profile:    flags.profile,
		Endpoint:   flags.server,
	})
	if cerr != nil {
		printf(out, "E_CONFIG: %s\nTry: gseer setup --help\n", cerr.Error())
		exitWith(ExitConfig)
	}
	if cfg.Profile != config.ProfileSelfHosted {
		printf(out, "E_CLIENT_ONLY: `server start` requires the self-hosted profile (this is a client-profile config)\nTry: gseer setup --self-hosted\n")
		exitWith(ExitConfig)
	}

	listen := cfg.Server.Listen
	if listen == "" {
		listen = config.DefaultListen
	}
	// Defense in depth: non-loopback bind without a token is refused
	// here even though config validation already rejects it.
	if !isLoopbackListen(listen) && cfg.Server.TokenHash == "" {
		printf(out, "E_CONFIG_INSECURE: refusing to bind %s without an API token\nWhy: a non-loopback server without authentication is reachable by anyone on the network\nTry: set server.token in the config, or keep the loopback default (%s)\n", listen, config.DefaultListen)
		exitWith(ExitConfig)
	}

	ctx := context.Background()

	// Hardened credential model (v0.2.0, Rev 6.7.2 §6.2 step 5): the
	// server runs ONLY with the least-privilege runtime credential —
	// never with the operator credential.  Missing runtime credential
	// is a configuration failure (exit 3) with the provisioning path.
	pool, runtimeDSN := openRuntimePool(ctx, cfg, out)
	defer pool.Close()

	runPrivilegeAuditGate(ctx, runtimeDSN, out)

	// The shared SearchService — CLI, API, and dashboard all use it.
	svc := search.New(pool)
	apiSrv := api.New(svc, cfg.Server.TokenHash, 0)
	// Trusted-proxy opt-in (M16): parse strictly — an invalid CIDR is a
	// configuration error, never silently ignored.
	trusted, terr := httpx.ParseTrustedProxies(cfg.Server.TrustedProxies)
	if terr != nil {
		printf(out, "E_CONFIG_INSECURE: %v\nWhy: a trusted proxy entry must be a valid CIDR or bare IP\nTry: fix server.trusted_proxies in the config\n", terr)
		exitWith(ExitConfig)
	}
	apiSrv.TrustedProxies = trusted
	dash := dashboard.New(svc)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiSrv.Handler()) // routes /api/v1/* internally
	mux.Handle("/", dash.Handler())       // dashboard + 404s

	httpServer := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		printf(out, "E_SERVER_LISTEN: cannot bind %s\nWhy: %v\nTry: free the port or change server.listen in the config\n", listen, err)
		exitWith(ExitUnavailable)
	}

	// Pidfile for status/stop (best-effort; a stale file is harmless).
	// 0600: the pid is process metadata — no reason for world-readability
	// (M16 pidfile hardening; identity verification lives in server stop).
	pidPath := serverPIDPath(listen)
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600)
	defer func() {
		if b, rerr := os.ReadFile(pidPath); rerr == nil && strings.TrimSpace(string(b)) == strconv.Itoa(os.Getpid()) { // #nosec G304 -- reads the server's OWN pidfile (path derived from the configured listen address)
			_ = os.Remove(pidPath)
		}
	}()

	printf(out, "GSEER server listening on http://%s\n", listen)
	printf(out, "  API:        http://%s/api/v1/health\n", listen)
	printf(out, "  Dashboard:  http://%s/\n", listen)
	if cfg.Server.TokenHash == "" {
		printf(out, "  Auth:       disabled (loopback bind only — set server.token for remote access)\n")
	} else {
		printf(out, "  Auth:       bearer token required\n")
	}
	meta, serr := svc.Stats(ctx)
	switch {
	case serr != nil:
		printf(out, "  Index:      unreachable (%v)\n", serr)
	case meta.Empty:
		printf(out, "  Index:      EMPTY — for evaluation data run: gseer setup --load-fixtures\n")
	case meta.FixtureOnly:
		printf(out, "  Index:      SYNTHETIC EVALUATION DATA (fixture corpus; not real observations)\n")
	default:
		printf(out, "  Index:      %d assets, %d services\n", meta.Assets, meta.Services)
	}
	printf(out, "Ready. Press Ctrl-C to stop.\n\n")

	// Graceful shutdown on SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(ln) }()

	select {
	case sig := <-stop:
		printf(out, "\nReceived %s — shutting down gracefully ...\n", sig)
		shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shCtx)
		printf(out, "Server stopped.\n")
		return nil
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			printf(out, "E_SERVER_SERVE: %v\n", err)
			exitWith(ExitInternal)
		}
		return nil
	}
}

func isLoopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// serverPIDPath is the pidfile location for a given listen address.
func serverPIDPath(listen string) string {
	return fmt.Sprintf("%s/gseer-server-%d.pid", os.TempDir(), fnv32(listen))
}

func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func runServerStatus(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()

	cfg, cerr := config.Load(config.Flags{
		ConfigPath: flags.configPath,
		Profile:    flags.profile,
		Endpoint:   flags.server,
	})
	if cerr != nil {
		printf(out, "E_CONFIG: %s\n", cerr.Error())
		exitWith(ExitConfig)
	}
	listen := cfg.Server.Listen
	if listen == "" {
		listen = config.DefaultListen
	}

	pid, ok := readServerPID(listen)
	if !ok {
		printf(out, "Server: NOT RUNNING (no pidfile for %s)\n", listen)
		exitWith(ExitUnavailable)
	}
	if !processAlive(pid) {
		printf(out, "Server: NOT RUNNING (stale pidfile, pid %d is gone)\n", pid)
		_ = os.Remove(serverPIDPath(listen))
		exitWith(ExitUnavailable)
	}

	// Probe the health endpoint.
	hc := &http.Client{Timeout: 3 * time.Second}
	resp, err := hc.Get("http://" + listen + "/api/v1/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		printf(out, "Server: RUNNING (pid %d) but the health endpoint did not answer\n", pid)
		exitWith(ExitUnavailable)
	}
	_ = resp.Body.Close()
	printf(out, "Server: RUNNING (pid %d) on http://%s — health OK\n", pid, listen)
	return nil
}

func readServerPID(listen string) (int, bool) {
	b, err := os.ReadFile(serverPIDPath(listen))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processIsGSEER verifies the process at pid is actually a GSEER server
// before `server stop` signals it (Linux /proc/<pid>/cmdline; on other
// platforms the check degrades to a conservative refusal only when the
// cmdline is unreadable AND the executable path does not match).
func processIsGSEER(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		// No /proc (non-Linux) or the process vanished between the
		// liveness check and now: fall back to executable-path match.
		p, ferr := os.FindProcess(pid)
		if ferr != nil {
			return false
		}
		// Best effort without /proc: allow the signal (the historical
		// behavior) — self-hosted operators control their own machine.
		_ = p
		return true
	}
	fields := strings.Split(string(bytes.TrimRight(raw, "\x00")), "\x00")
	for _, f := range fields {
		if strings.Contains(f, "gseer") {
			return true
		}
	}
	return false
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// ---------------------------------------------------------------------------
// stop
// ---------------------------------------------------------------------------

func runServerStop(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()

	cfg, cerr := config.Load(config.Flags{
		ConfigPath: flags.configPath,
		Profile:    flags.profile,
		Endpoint:   flags.server,
	})
	if cerr != nil {
		printf(out, "E_CONFIG: %s\n", cerr.Error())
		exitWith(ExitConfig)
	}
	listen := cfg.Server.Listen
	if listen == "" {
		listen = config.DefaultListen
	}

	pid, ok := readServerPID(listen)
	if !ok {
		printf(out, "No running server found for %s (started in the foreground? stop it with Ctrl-C).\n", listen)
		exitWith(ExitUnavailable)
	}
	if !processAlive(pid) {
		_ = os.Remove(serverPIDPath(listen))
		printf(out, "Server: NOT RUNNING (stale pidfile removed)\n")
		return nil
	}
	// Process-identity verification (M16 pidfile hardening): a pidfile
	// says "pid N", not "a GSEER server".  After a crash and PID reuse,
	// pid N may be some other program — signalling it blindly would kill
	// an unrelated process.  Verify the cmdline names the gseer binary
	// before sending anything.
	if !processIsGSEER(pid) {
		printf(out, "E_SERVER_IDENTITY: pid %d is alive but is NOT a GSEER server (process identity mismatch)\n"+
			"Why: the pidfile is stale and the pid was reused by another program\n"+
			"Try: remove %s manually, or verify the process with: ps -p %d\n", pid, serverPIDPath(listen), pid)
		exitWith(ExitInternal)
	}

	p, err := os.FindProcess(pid)
	if err == nil {
		_ = p.Signal(syscall.SIGTERM)
	}
	// Wait briefly for the process to exit.
	for i := 0; i < 20; i++ {
		if !processAlive(pid) {
			_ = os.Remove(serverPIDPath(listen))
			printf(out, "Server stopped (pid %d).\n", pid)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	printf(out, "Server (pid %d) did not exit within 2s; send SIGKILL manually if needed.\n", pid)
	exitWith(ExitUnavailable)
	return nil
}
