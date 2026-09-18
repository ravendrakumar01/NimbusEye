// Command prober runs the checks NimbusEye performs itself: websites, ports,
// DNS records, TLS certificates and domain expiry.
//
//	nimbuseye-prober --once
//	nimbuseye-prober --interval 30s
//
// It picks up whatever is due each pass, so the per-monitor check interval set in
// the UI is what governs frequency; this interval is only how often the prober
// looks for work.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nimbuseye/internal/probe"
)

func main() {
	var (
		dsn         = flag.String("dsn", "", "PostgreSQL DSN; defaults to $NIMBUSEYE_DSN")
		tenant      = flag.String("tenant", "default", "tenant slug to probe")
		once        = flag.Bool("once", false, "run a single pass and exit")
		interval    = flag.Duration("interval", 30*time.Second, "how often to look for due checks")
		concurrency = flag.Int("concurrency", 20, "how many checks to run at once")
		timeout     = flag.Duration("timeout", 4*time.Minute, "maximum duration of one pass")
		logLevel    = flag.String("log-level", "info", "debug | info | warn | error")
	)
	flag.Parse()

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "invalid --log-level %q\n", *logLevel)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	target := *dsn
	if target == "" {
		target = os.Getenv("NIMBUSEYE_DSN")
	}
	if target == "" {
		log.Error("no DSN: pass --dsn or set NIMBUSEYE_DSN")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	runner, err := probe.Open(openCtx, target, *tenant, *concurrency, log)
	cancel()
	if err != nil {
		log.Error("cannot connect", "err", err)
		os.Exit(1)
	}
	defer runner.Close()

	runOnce := func() {
		runCtx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		rep, err := runner.Run(runCtx)
		took := rep.FinishedAt.Sub(rep.StartedAt).Round(time.Millisecond)
		if err != nil {
			log.Error("probe pass failed", "err", err, "took", took.String())
			return
		}
		if rep.Checked == 0 {
			log.Debug("nothing due", "took", took.String())
			return
		}
		log.Info("probe pass complete",
			"checked", rep.Checked, "up", rep.Up, "down", rep.Down,
			"unknown", rep.Unknown, "changed", rep.Changed, "samples", rep.Samples,
			"took", took.String())
	}

	if *once {
		runOnce()
		return
	}
	runOnce()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	log.Info("probing continuously", "interval", interval.String(), "concurrency", *concurrency)
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case <-ticker.C:
			runOnce()
		}
	}
}
