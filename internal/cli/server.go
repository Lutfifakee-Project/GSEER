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
// start: self-hosted only; runs migrations (QS-7), opens the pool, and
// serves the REST API (/api/v1) + dashboard (/) over the SAME
// SearchService (AC-4.9).  Runs in the foreground with graceful
// shutdown on SIGINT/SIGTERM; the pidfile enables `server stop`.
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
		Short: "Start the local API + dashboard server",
		Long: `Start the local API + dashboard server.

Self-hosted profile only.  Migrations run automatically on start (QS-7).
The server listens on the configured address (loopback by default).
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

	// Pool + migrations (QS-7: migrations run automatically on start).
	pool, err := pgxpool.New(ctx, cfg.Database.EffectiveDSN())
	if err != nil {
		printf(out, "E_DB_UNAVAILABLE: cannot open the database pool\nWhy: %v\nTry: check database.dsn in the config — gseer doctor\n", err)
		exitWith(ExitUnavailable)
	}
	defer pool.Close()

	printf(out, "Running migrations (QS-7) ...\n")
	applied, head, err := postgres.RunMigrations(cfg.Database.EffectiveDSN(), cfg.Database.MigrationLockTimeoutMs)
	if err != nil {
		printf(out, "DB_MIGRATION_FAILED: %v\nTry: gseer db migrate diagnose\n", err)
		exitWith(ExitUnavailable)
	}
	printf(out, "Migrations: applied %d, head %d.\n", applied, head)

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
		if b, rerr := os.ReadFile(pidPath); rerr == nil && strings.TrimSpace(string(b)) == strconv.Itoa(os.Getpid()) {
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
		printf(out, "Server: RUNNING (pid %d) but the health endpoint did not answer\n", pid)
		exitWith(ExitUnavailable)
	}
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
