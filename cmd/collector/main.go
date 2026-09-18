// Command collector runs cloud discovery and metric collection.
//
// It reads account configuration from a JSON file on the server, never from the
// command line, so credentials paths and tenancy identifiers do not end up in
// shell history or process listings.
//
//	nimbuseye-collector --account /etc/nimbuseye/accounts/oci-prod.json --dry-run
//	nimbuseye-collector --account /etc/nimbuseye/accounts/oci-prod.json \
//	    --api http://127.0.0.1:8080 --ingest-token-file /etc/nimbuseye/ingest.token
//
// With --interval it stays running and collects on a schedule, which is how the
// systemd unit invokes it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"nimbuseye/internal/collect"
)

// accountFile is the on-disk shape of one cloud account's collector config.
//
// This mirrors what the Admin UI stores, and deliberately holds a path to the
// signing key rather than the key itself.
type accountFile struct {
	AccountID      string   `json:"account_id"`
	Provider       string   `json:"provider"`
	TenancyOCID    string   `json:"tenancy_ocid"`
	UserOCID       string   `json:"user_ocid"`
	Fingerprint    string   `json:"fingerprint"`
	PrivateKeyPath string   `json:"private_key_path"`
	Regions        []string `json:"regions"`
	Compartments   []string `json:"compartments"`
}

func main() {
	var (
		accountPath = flag.String("account", "", "path to the account configuration JSON (required)")
		apiBase     = flag.String("api", "http://127.0.0.1:8080", "NimbusEye API base URL")
		tokenFile   = flag.String("ingest-token-file", "", "file containing the shared ingest token")
		dryRun      = flag.Bool("dry-run", false, "collect but do not send anything; log what would be stored")
		withMetrics = flag.Bool("metrics", true, "collect metrics after discovery")
		interval    = flag.Duration("interval", 0, "run repeatedly at this interval; 0 means run once and exit")
		window      = flag.Duration("metric-window", 30*time.Minute,
			"how far back to read metrics each run; wider than the interval so late-arriving points are not lost")
		timeout  = flag.Duration("timeout", 15*time.Minute, "maximum duration of a single run")
		logLevel = flag.String("log-level", "info", "debug | info | warn | error")
	)
	flag.Parse()

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "invalid --log-level %q\n", *logLevel)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	if *accountPath == "" {
		log.Error("--account is required")
		flag.Usage()
		os.Exit(2)
	}

	acctFile, err := loadAccount(*accountPath)
	if err != nil {
		log.Error("cannot load account configuration", "err", err)
		os.Exit(1)
	}
	if acctFile.Provider != "oci" {
		log.Error("only the OCI collector is implemented", "provider", acctFile.Provider)
		os.Exit(1)
	}

	var sink collect.Sink
	if *dryRun {
		sink = collect.LogSink{Log: log}
		log.Info("dry run: nothing will be stored")
	} else {
		token, err := readToken(*tokenFile)
		if err != nil {
			log.Error("cannot read ingest token", "err", err)
			os.Exit(1)
		}
		sink = collect.NewHTTPSink(strings.TrimRight(*apiBase, "/"), token)
	}

	acct := collect.OCIAccount{
		AccountID:      acctFile.AccountID,
		TenancyOCID:    acctFile.TenancyOCID,
		UserOCID:       acctFile.UserOCID,
		Fingerprint:    acctFile.Fingerprint,
		PrivateKeyPath: acctFile.PrivateKeyPath,
		Regions:        acctFile.Regions,
		Compartments:   acctFile.Compartments,
		MetricWindow:   *window,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runOnce := func() error {
		runCtx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		start := time.Now()
		rep, err := collect.RunOCI(runCtx, acct, sink, log, *withMetrics)
		log.Info("run finished",
			"state", rep.State, "discovered", rep.Discovered, "mapped", rep.Mapped,
			"ignored", rep.Ignored, "retired", rep.Retired,
			"confirmed_up", rep.Confirmed, "unmapped", len(rep.UnmappedTypes),
			"samples", rep.MetricSamples, "took", time.Since(start).Round(time.Millisecond).String())
		if len(rep.UnmappedTypes) > 0 {
			// Worth surfacing at info: it is the list of OCI services this build
			// cannot monitor yet.
			log.Info("unmapped OCI resource types", "types", rep.UnmappedTypes)
		}
		return err
	}

	if *interval <= 0 {
		if err := runOnce(); err != nil {
			log.Error("collection failed", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := runOnce(); err != nil {
		// A failed first run is logged but does not stop the loop: a transient
		// provider outage should not require the service to be restarted.
		log.Error("initial collection failed", "err", err)
	}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case <-ticker.C:
			if err := runOnce(); err != nil {
				log.Error("collection failed", "err", err)
			}
		}
	}
}

func loadAccount(path string) (accountFile, error) {
	var a accountFile
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return a, fmt.Errorf("%s does not exist", path)
		}
		return a, err
	}
	// The file names a private key and identifies a tenancy, so it must not be
	// world-readable. Group access is permitted because the deployment pattern is
	// root-owned, group-readable by the dedicated service group.
	if fi.Mode().Perm()&0o007 != 0 {
		return a, fmt.Errorf("%s is world-accessible (mode %o); run chmod 640", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return a, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return a, fmt.Errorf("%s is not valid account JSON: %w", path, err)
	}
	if a.AccountID == "" {
		return a, errors.New("account_id is required")
	}
	return a, nil
}

func readToken(path string) (string, error) {
	if path == "" {
		return "", errors.New("--ingest-token-file is required unless --dry-run is set")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.Mode().Perm()&0o007 != 0 {
		return "", fmt.Errorf("%s is world-accessible (mode %o); run chmod 640", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if len(tok) < 32 {
		return "", errors.New("ingest token is too short; use at least 32 characters")
	}
	return tok, nil
}
