// Command alerter evaluates threshold profiles and maintains alert, outage and
// availability state.
//
//	nimbuseye-alerter --once                    # one pass, then exit
//	nimbuseye-alerter --interval 1m             # run continuously
//
// The DSN comes from $NIMBUSEYE_DSN unless --dsn is given, so credentials stay
// out of shell history and process listings.
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

	"nimbuseye/internal/alert"
)

func main() {
	var (
		dsn      = flag.String("dsn", "", "PostgreSQL DSN; defaults to $NIMBUSEYE_DSN")
		tenant   = flag.String("tenant", "default", "tenant slug to evaluate")
		once     = flag.Bool("once", false, "run a single pass and exit")
		interval = flag.Duration("interval", time.Minute, "evaluation interval")
		timeout  = flag.Duration("timeout", 5*time.Minute, "maximum duration of one pass")
		logLevel = flag.String("log-level", "info", "debug | info | warn | error")
		rollup   = flag.Int("rollup-days", 1, "how many days back to recompute availability; "+
			"raise it once after the rollup definition changes")
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
		log.Error("no DSN: pass --dsn or set NIMBUSEYE_DSN (see ~/.nimbuseye/db.env)")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ev, err := alert.Open(openCtx, target, *tenant, log)
	if err == nil && ev != nil {
		ev.RollupDays = *rollup
	}
	cancel()
	if err != nil {
		log.Error("cannot connect", "err", err)
		os.Exit(1)
	}
	defer ev.Close()

	runOnce := func() error {
		runCtx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		rep, err := ev.Run(runCtx)
		took := rep.FinishedAt.Sub(rep.StartedAt).Round(time.Millisecond)
		if err != nil {
			log.Error("evaluation failed", "err", err, "took", took.String())
			return err
		}
		// One line per pass at info. Anything that changed state is worth seeing;
		// a quiet estate produces zeroes, which is the point.
		log.Info("evaluation complete",
			"resources", rep.Resources, "rules", rep.Rules,
			"opened", rep.Opened, "updated", rep.Updated, "resolved", rep.Resolved,
			"suppressed", rep.Suppressed, "escalated", rep.Escalated,
			"outages_opened", rep.Outages, "outages_closed", rep.Closed,
			"notifications_queued", rep.Notified, "rollup_rows", rep.RollupDays,
			"took", took.String())
		return nil
	}

	if *once {
		if err := runOnce(); err != nil {
			os.Exit(1)
		}
		return
	}

	if err := runOnce(); err != nil {
		// A failed first pass does not stop the loop: a transient database blip
		// should not require restarting the service.
		log.Warn("continuing after failed initial pass")
	}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	log.Info("evaluating continuously", "interval", interval.String())
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case <-ticker.C:
			_ = runOnce()
		}
	}
}
