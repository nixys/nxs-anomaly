package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	// Schedules resolve on-call in their own IANA timezone. The production
	// image is distroless and carries no /usr/share/zoneinfo, so the database
	// is embedded here rather than left to the host.
	_ "time/tzdata"

	"github.com/nixys/nxs-anomaly/internal/engine"
	"github.com/nixys/nxs-anomaly/internal/server"
	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/tracing"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// setupLogging configures the default slog logger from LOG_LEVEL
// (debug|info|warn|error, default info) and LOG_FORMAT (text|json, default text).
// Without it the default Info level silently drops every slog.Debug call, with no
// way to enable them in production.
func setupLogging() {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "json") {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

// commandOrder is the order commands are listed in the usage line; commands maps
// each to its handler. One list instead of a switch plus a hand-written usage
// string, which drifted apart the moment a command was added to only one.
var commandOrder = []string{
	"serve", "healthcheck", "run-worker", "run-escalations", "run-report",
	"seed-demo", "print-state", "history", "alerts", "notifications",
}

var commands = map[string]func([]string){
	"serve":           cmdServe,
	"healthcheck":     cmdHealthcheck,
	"run-worker":      cmdRunWorker,
	"run-escalations": cmdRunEscalations,
	"run-report":      cmdRunReport,
	"seed-demo":       cmdSeedDemo,
	"print-state":     cmdPrintState,
	"history":         cmdHistory,
	"alerts":          cmdAlerts,
	"notifications":   cmdNotifications,
}

func main() {
	setupLogging()
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run dispatches a subcommand and returns the process exit code. It is separate
// from main so dispatch, usage text and exit codes are testable without spawning
// a process; the handlers themselves still exit on their own errors.
func run(args []string, stderr io.Writer) int {
	if len(args) < 1 {
		// Writes to an io.Writer rather than os.Stderr so a test can read them;
		// a failed write to stderr is not actionable.
		_, _ = fmt.Fprintln(stderr, "usage: nxs-anomaly <command> [flags]")
		_, _ = fmt.Fprintln(stderr, "commands: "+strings.Join(commandOrder, "  "))
		return 1
	}
	handler, ok := commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		return 1
	}
	handler(args[1:])
	return 0
}

func cmdHealthcheck(args []string) {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	url := fs.String("url", "http://127.0.0.1:8080/health", "health endpoint URL")
	timeout := fs.Duration("timeout", 2*time.Second, "request timeout")
	parseFlags(fs, args)

	client := &http.Client{Timeout: *timeout}
	resp, err := client.Get(*url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "healthcheck failed: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	host := fs.String("host", "0.0.0.0", "listen host")
	port := fs.String("port", "8080", "listen port")
	pollInterval := fs.Int("poll-interval", 5, "worker poll interval in seconds")
	noScheduler := fs.Bool("no-scheduler", false, "disable background scheduler")
	apiKey := fs.String("api-key", os.Getenv("NXS_ANOMALY_API_KEY"), "API key for management endpoints")
	tlsCert := fs.String("tls-cert", os.Getenv("NXS_ANOMALY_TLS_CERT"), "path to TLS certificate file")
	tlsKey := fs.String("tls-key", os.Getenv("NXS_ANOMALY_TLS_KEY"), "path to TLS private key file")
	parseFlags(fs, args)

	cfg := server.ConfigFromEnv()
	if *host != "0.0.0.0" || *port != "8080" {
		cfg.Addr = *host + ":" + *port
	}
	if *pollInterval != 5 {
		cfg.PollInterval = time.Duration(*pollInterval) * time.Second
	}
	if *noScheduler {
		cfg.StartScheduler = false
	}
	if *apiKey != "" {
		cfg.APIKey = *apiKey
	}
	if *tlsCert != "" {
		cfg.TLSCert = *tlsCert
	}
	if *tlsKey != "" {
		cfg.TLSKey = *tlsKey
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Before the store, so a slow exporter handshake does not sit between the
	// database being up and the first span being recordable. Deferred shutdown
	// flushes whatever is batched when the process is asked to stop.
	shutdownTracing, _ := tracing.Init(ctx, server.Version)
	defer tracing.Shutdown(shutdownTracing)

	// Listen before waiting for the database and migrations, so a liveness
	// probe sees a live process rather than a closed port.
	fd, err := server.OpenFrontdoor(cfg.Addr, cfg, cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		slog.Error("listen failed", "addr", cfg.Addr, "err", err)
		os.Exit(1)
	}
	cfg.Frontdoor = fd

	s, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	eng := newEngine(s)
	defer eng.Close()
	if err := server.New(ctx, s, eng, cfg); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

func cmdRunWorker(args []string) {
	fs := flag.NewFlagSet("run-worker", flag.ExitOnError)
	pollInterval := fs.Int("poll-interval", 5, "poll interval in seconds")
	once := fs.Bool("once", false, "run a single worker cycle and exit")
	workerAddr := fs.String("worker-addr", "", "address for the worker telemetry endpoint (/live, /ready, /metrics); overrides NXS_ANOMALY_WORKER_ADDR")
	parseFlags(fs, args)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// The worker is the other half of the trace: it produces the escalation and
	// delivery spans that a webhook's ingest span points at.
	shutdownTracing, _ := tracing.Init(ctx, server.Version)
	defer tracing.Shutdown(shutdownTracing)

	cfg := server.ConfigFromEnv()
	if *pollInterval != 5 {
		cfg.PollInterval = time.Duration(*pollInterval) * time.Second
	}
	if *workerAddr != "" {
		cfg.WorkerAddr = *workerAddr
	}
	if !*once {
		// Listen before waiting for the database and migrations, so a liveness
		// probe sees a live process rather than a closed port.
		fd, err := server.OpenFrontdoor(cfg.WorkerAddr, cfg, "", "")
		if err != nil {
			slog.Error("listen failed", "addr", cfg.WorkerAddr, "err", err)
			os.Exit(1)
		}
		cfg.WorkerFrontdoor = fd
	}

	s, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	eng := newEngine(s)
	defer eng.Close()

	if *once {
		// One-shot mode is for CLI/diagnostics; it needs no telemetry endpoint.
		eng.SetReferenceCacheTTL(time.Duration(*pollInterval) * time.Second)
		result, err := eng.RunWorkerCycle(context.WithoutCancel(ctx))
		if err != nil {
			slog.Error("worker cycle failed", "err", err)
			os.Exit(1)
		}
		printJSON(result)
		return
	}

	if err := server.RunWorker(ctx, s, eng, cfg); err != nil {
		slog.Error("worker error", "err", err)
		os.Exit(1)
	}
}

func cmdRunEscalations(args []string) {
	fs := flag.NewFlagSet("run-escalations", flag.ExitOnError)
	parseFlags(fs, args)

	ctx := context.Background()
	s, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	eng := engine.New(s)
	result, err := eng.ProcessDueEscalations(ctx)
	if err != nil {
		slog.Error("process escalations failed", "err", err)
		os.Exit(1)
	}
	printJSON(result)
}

// cmdRunReport generates and delivers the on-call quality digest (Enterprise;
// see internal/engine/report.go). Intended to run from the weekly Helm
// CronJob, but safe to run by hand: a period already reported for a team is
// returned as-is rather than re-sent (GenerateAndDeliverOnCallQualityReport is
// idempotent by team+period).
func cmdRunReport(args []string) {
	fs := flag.NewFlagSet("run-report", flag.ExitOnError)
	team := fs.String("team", "", "comma-separated team IDs to report on; empty means one installation-wide report")
	allTeams := fs.Bool("all-teams", false, "generate one report per existing team instead of --team")
	parseFlags(fs, args)

	ctx := context.Background()
	s, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	eng := newEngine(s)
	defer eng.Close()

	var teamIDs []string
	switch {
	case *allTeams:
		teams, err := eng.ListCollection(ctx, "teams")
		if err != nil {
			slog.Error("list teams failed", "err", err)
			os.Exit(1)
		}
		for _, t := range teams {
			teamIDs = append(teamIDs, strVal(t, "id"))
		}
	case *team != "":
		for _, id := range strings.Split(*team, ",") {
			if id = strings.TrimSpace(id); id != "" {
				teamIDs = append(teamIDs, id)
			}
		}
	default:
		teamIDs = []string{""}
	}

	now := time.Now().UTC()
	results := make([]map[string]any, 0, len(teamIDs))
	failed := false
	for _, id := range teamIDs {
		report, err := eng.GenerateAndDeliverOnCallQualityReport(ctx, id, now)
		if err != nil {
			slog.Error("generate report failed", "team_id", id, "err", err)
			failed = true
			continue
		}
		results = append(results, report)
	}
	printJSON(results)
	if failed {
		os.Exit(1)
	}
}

func cmdSeedDemo(args []string) {
	fs := flag.NewFlagSet("seed-demo", flag.ExitOnError)
	force := fs.Bool("force", false, "reset store before seeding")
	parseFlags(fs, args)

	ctx := context.Background()
	s, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	eng := engine.New(s)
	result, err := eng.SeedDemo(ctx, *force)
	if err != nil {
		slog.Error("seed-demo failed", "err", err)
		os.Exit(1)
	}
	printJSON(result)
}

func cmdPrintState(args []string) {
	fs := flag.NewFlagSet("print-state", flag.ExitOnError)
	parseFlags(fs, args)

	ctx := context.Background()
	s, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	collections := make([]string, 0, len(store.EntityTables))
	for k := range store.EntityTables {
		collections = append(collections, k)
	}
	state, err := s.ReadCollections(ctx, collections)
	if err != nil {
		slog.Error("read state failed", "err", err)
		os.Exit(1)
	}
	printJSON(state)
}

func cmdHistory(args []string) {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	limit := fs.Int("limit", 20, "max groups to show")
	severity := fs.String("severity", "", "filter by severity")
	status := fs.String("status", "", "filter by status")
	integration := fs.String("integration", "", "filter by integration ID")
	from := fs.String("from", "", "start time filter (ISO8601)")
	to := fs.String("to", "", "end time filter (ISO8601)")
	parseFlags(fs, args)

	ctx := context.Background()
	s, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	eng := engine.New(s)
	filters := map[string]any{
		"limit":       *limit,
		"severity":    nilIfEmpty(*severity),
		"status":      nilIfEmpty(*status),
		"integration": nilIfEmpty(*integration),
		"from":        nilIfEmpty(*from),
		"to":          nilIfEmpty(*to),
	}
	result, err := eng.GetHistory(ctx, filters)
	if err != nil {
		slog.Error("get history failed", "err", err)
		os.Exit(1)
	}
	items, _ := result["items"].([]map[string]any)
	if len(items) > *limit {
		items = items[:*limit]
	}
	rows := make([]map[string]string, 0, len(items))
	for _, g := range items {
		ag, _ := g["alert_group"].(map[string]any)
		ntfs, _ := g["notifications"].([]map[string]any)
		rows = append(rows, map[string]string{
			"id":         truncate(strVal(ag, "id"), 16),
			"title":      truncate(strVal(ag, "title"), 38),
			"severity":   strVal(ag, "severity"),
			"status":     strVal(ag, "status"),
			"created_at": truncate(strVal(ag, "created_at"), 19),
			"notifs":     fmt.Sprintf("%d", len(ntfs)),
		})
	}
	printTable(rows, []string{"id", "title", "severity", "status", "created_at", "notifs"})
	count, _ := result["count"].(int)
	fmt.Printf("Total: %d (showing %d)\n", count, len(rows))
}

func cmdAlerts(args []string) {
	fs := flag.NewFlagSet("alerts", flag.ExitOnError)
	limit := fs.Int("limit", 20, "max alerts to show")
	status := fs.String("status", "", "filter by status")
	severity := fs.String("severity", "", "filter by severity")
	integration := fs.String("integration", "", "filter by integration ID")
	parseFlags(fs, args)

	ctx := context.Background()
	s, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	eng := engine.New(s)
	params := map[string]any{"limit": *limit}
	if *status != "" {
		params["status"] = *status
	}
	if *severity != "" {
		params["severity"] = *severity
	}
	if *integration != "" {
		params["integration_id"] = *integration
	}
	result, err := eng.ListCollectionPage(ctx, "alerts", params)
	if err != nil {
		slog.Error("list alerts failed", "err", err)
		os.Exit(1)
	}
	items, _ := result["items"].([]map[string]any)
	rows := make([]map[string]string, 0, len(items))
	for _, a := range items {
		rows = append(rows, map[string]string{
			"id":          truncate(strVal(a, "id"), 16),
			"title":       truncate(strVal(a, "title"), 38),
			"severity":    strVal(a, "severity"),
			"status":      strVal(a, "status"),
			"source":      strVal(a, "source"),
			"received_at": truncate(strVal(a, "received_at"), 19),
		})
	}
	printTable(rows, []string{"id", "title", "severity", "status", "source", "received_at"})
	total, _ := result["total"].(int)
	fmt.Printf("Total: %d (showing %d)\n", total, len(rows))
}

func cmdNotifications(args []string) {
	fs := flag.NewFlagSet("notifications", flag.ExitOnError)
	limit := fs.Int("limit", 20, "max notifications to show")
	status := fs.String("status", "", "filter by status")
	user := fs.String("user", "", "filter by user ID")
	group := fs.String("group", "", "filter by alert group ID")
	parseFlags(fs, args)

	ctx := context.Background()
	s, err := store.NewPostgreSQLStore(ctx)
	if err != nil {
		slog.Error("store init failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	eng := engine.New(s)
	params := map[string]any{"limit": *limit}
	if *status != "" {
		params["status"] = *status
	}
	if *user != "" {
		params["user_id"] = *user
	}
	if *group != "" {
		params["alert_group_id"] = *group
	}
	result, err := eng.ListCollectionPage(ctx, "notifications", params)
	if err != nil {
		slog.Error("list notifications failed", "err", err)
		os.Exit(1)
	}
	items, _ := result["items"].([]map[string]any)
	rows := make([]map[string]string, 0, len(items))
	for _, n := range items {
		uid := ""
		if v, ok := n["user_id"]; ok && v != nil {
			uid = fmt.Sprintf("%v", v)
		}
		rows = append(rows, map[string]string{
			"id":         truncate(strVal(n, "id"), 16),
			"channel":    strVal(n, "channel"),
			"status":     strVal(n, "status"),
			"user_id":    truncate(uid, 16),
			"retries":    fmt.Sprintf("%v", n["retry_count"]),
			"updated_at": truncate(strVal(n, "updated_at"), 19),
		})
	}
	printTable(rows, []string{"id", "channel", "status", "user_id", "retries", "updated_at"})
	total, _ := result["total"].(int)
	fmt.Printf("Total: %d (showing %d)\n", total, len(rows))
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		slog.Error("write JSON failed", "err", err)
		os.Exit(1)
	}
}

func parseFlags(fs *flag.FlagSet, args []string) {
	if err := fs.Parse(args); err != nil {
		slog.Error("parse flags failed", "command", fs.Name(), "err", err)
		os.Exit(2)
	}
}

func printTable(rows []map[string]string, columns []string) {
	if len(rows) == 0 {
		fmt.Println("(no items)")
		return
	}
	widths := make(map[string]int, len(columns))
	for _, col := range columns {
		widths[col] = len(col)
	}
	for _, row := range rows {
		for _, col := range columns {
			if l := utf8.RuneCountInString(row[col]); l > widths[col] {
				widths[col] = l
			}
		}
	}
	sep := func() string {
		s := "+-"
		for i, col := range columns {
			if i > 0 {
				s += "-+-"
			}
			for range widths[col] {
				s += "-"
			}
		}
		return s + "-+"
	}()
	fmt.Println(sep)
	header := "| "
	for i, col := range columns {
		if i > 0 {
			header += " | "
		}
		header += padRight(col, widths[col])
	}
	fmt.Println(header + " |")
	fmt.Println(sep)
	for _, row := range rows {
		line := "| "
		for i, col := range columns {
			if i > 0 {
				line += " | "
			}
			line += padRight(row[col], widths[col])
		}
		fmt.Println(line + " |")
	}
	fmt.Println(sep)
}

// padRight and truncate count runes, not bytes. Alert titles here are routinely
// Cyrillic, where a byte count both misaligns every column by the number of
// two-byte letters in it and cuts the last one in half, printing a replacement
// character into the operator's terminal.
func padRight(s string, n int) string {
	for i := utf8.RuneCountInString(s); i < n; i++ {
		s += " "
	}
	return s
}

func truncate(s string, n int) string {
	return utils.TruncateRunes(s, n)
}

func strVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
