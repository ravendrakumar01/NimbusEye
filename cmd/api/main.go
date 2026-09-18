// Command api serves the NimbusEye REST API.
//
// Two modes:
//
//	--mock        generate an in-memory estate; no database, no cloud credentials
//	(default)     read from PostgreSQL and VictoriaMetrics  [not yet implemented]
//
// Mock mode exists so the UI can be built and reviewed before any cloud account
// is connected. It is the mode to use for local development.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"
	"time"

	"nimbuseye/internal/auth"
	"nimbuseye/internal/demo"
	"nimbuseye/internal/httpapi"
	"nimbuseye/internal/mail"
	"nimbuseye/internal/migrate"
	"nimbuseye/internal/store"
	"nimbuseye/internal/store/pg"
)

var version = "0.1.0-dev"

func main() {
	var (
		addr = flag.String("addr", "127.0.0.1:8080",
			"listen address; defaults to loopback so a dev server is never exposed by accident")
		mock      = flag.Bool("mock", false, "serve generated demo data instead of a database")
		tenant    = flag.String("tenant", "default", "tenant slug the API serves")
		mockCount = flag.Int("mock-resources", 2000, "number of demo resources to generate")
		mockSeed  = flag.Int64("mock-seed", 20260901, "demo data seed; same seed gives the same estate")
		devOrigin = flag.String("dev-cors-origin", "",
			"allow browser requests from this exact origin, e.g. http://localhost:5173 (development only)")
		doMigrate = flag.Bool("migrate", false,
			"apply database migrations and exit; uses NIMBUSEYE_MIGRATE_DSN")
		dsn = flag.String("dsn", "",
			"PostgreSQL DSN; defaults to $NIMBUSEYE_DSN, or $NIMBUSEYE_MIGRATE_DSN with --migrate")
		ingestTokenFile = flag.String("ingest-token-file", "",
			"file containing the shared token the collector authenticates with; without it the ingest endpoints stay disabled")
		createUser = flag.String("create-user", "",
			"provision a user and exit, e.g. --create-user admin@example.com; the password is read from stdin")
		userRole        = flag.String("role", "owner", "role for --create-user: owner | admin | operator | viewer")
		userName        = flag.String("name", "", "display name for --create-user")
		insecureCookies = flag.Bool("insecure-cookies", false,
			"omit the Secure flag on session cookies; for plain-HTTP local development only")
		logLevel = flag.String("log-level", "info", "debug | info | warn | error")
	)
	flag.Parse()

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "invalid --log-level %q\n", *logLevel)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	if *doMigrate {
		target := *dsn
		if target == "" {
			target = os.Getenv("NIMBUSEYE_MIGRATE_DSN")
		}
		if target == "" {
			log.Error("no DSN: pass --dsn or set NIMBUSEYE_MIGRATE_DSN (see ~/.nimbuseye/db.env)")
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		res, err := migrate.Run(ctx, target, log)
		if err != nil {
			log.Error("migration failed", "err", err)
			os.Exit(1)
		}
		log.Info("migrations complete",
			"applied", len(res.Applied), "already_applied", len(res.Skipped))
		return
	}

	// --create-user runs before anything else and exits: it is administration, not
	// a server mode.
	if *createUser != "" {
		target := *dsn
		if target == "" {
			target = os.Getenv("NIMBUSEYE_DSN")
		}
		if target == "" {
			log.Error("no DSN: pass --dsn or set NIMBUSEYE_DSN")
			os.Exit(2)
		}
		if err := provisionUser(target, *tenant, *createUser, *userName, *userRole, log); err != nil {
			log.Error("could not create user", "err", err)
			os.Exit(1)
		}
		return
	}

	var backend store.Store
	var authStore *auth.Store
	if *mock {
		start := time.Now()
		st := demo.New(*mockSeed, *mockCount)
		sum := st.Summary()
		log.Info("demo estate generated",
			"resources", sum.Total, "seed", *mockSeed,
			"down", sum.ByStatus["down"], "critical", sum.ByStatus["critical"],
			"trouble", sum.ByStatus["trouble"],
			"open_alarms", sum.OpenAlarms["down"]+sum.OpenAlarms["critical"]+sum.OpenAlarms["trouble"],
			"took", time.Since(start).String())
		log.Warn("serving generated data: nothing here reflects real infrastructure")
		backend = st
	} else {
		target := *dsn
		if target == "" {
			target = os.Getenv("NIMBUSEYE_DSN")
		}
		if target == "" {
			log.Error("no DSN: pass --dsn, set NIMBUSEYE_DSN (see ~/.nimbuseye/db.env), or use --mock")
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		pgStore, err := pg.Open(ctx, target, *tenant, log)
		cancel()
		if err != nil {
			log.Error("cannot open database", "err", err)
			os.Exit(1)
		}
		defer func() { _ = pgStore.Close(context.Background()) }()
		authStore = auth.NewStore(pgStore.Pool())
		if n, err := authStore.CountUsers(context.Background(), *tenant); err == nil && n == 0 {
			// Loud, because an instance with no users cannot be signed into and the
			// login page would otherwise just reject every attempt.
			log.Warn("no users exist yet; create one with: nimbuseye-api --create-user you@example.com --name 'Your Name'")
		}
		sum := pgStore.Summary()
		log.Info("database ready", "resources", sum.Total, "open_alarms",
			sum.OpenAlarms["down"]+sum.OpenAlarms["critical"]+sum.OpenAlarms["trouble"])
		backend = pgStore
	}

	var ingestToken string
	if *ingestTokenFile != "" {
		tok, err := readTokenFile(*ingestTokenFile)
		if err != nil {
			log.Error("cannot read ingest token", "err", err)
			os.Exit(1)
		}
		ingestToken = tok
	}

	// Built from the environment so the password never appears on a command line.
	// A missing relay is not fatal: only the features that send email degrade.
	var mailer *mail.Sender
	if host := os.Getenv("NIMBUSEYE_SMTP_HOST"); host != "" {
		port := 587
		if v := os.Getenv("NIMBUSEYE_SMTP_PORT"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				port = n
			}
		}
		m, err := mail.New(mail.Config{
			Host:         host,
			Port:         port,
			User:         os.Getenv("NIMBUSEYE_SMTP_USER"),
			PasswordFile: os.Getenv("NIMBUSEYE_SMTP_PASSWORD_FILE"),
			From:         os.Getenv("NIMBUSEYE_SMTP_FROM"),
			FromName:     os.Getenv("NIMBUSEYE_SMTP_FROM_NAME"),
			STARTTLS:     os.Getenv("NIMBUSEYE_SMTP_STARTTLS") != "false",
		})
		if err != nil {
			log.Error("SMTP is configured but unusable", "err", err)
			os.Exit(1)
		}
		mailer = m
		log.Info("mail relay configured", "host", host, "port", port, "from", m.From())
	} else {
		log.Warn("no SMTP relay configured: password reset and email notifications are unavailable")
	}

	baseURL := os.Getenv("NIMBUSEYE_BASE_URL")

	srv := httpapi.New(backend, authStore, mailer, httpapi.Config{
		DevCORSOrigin: *devOrigin,
		Mock:          *mock,
		IngestToken:   ingestToken,
		TenantSlug:    *tenant,
		SecureCookies: !*insecureCookies,
		BaseURL:       baseURL,
		Version:       version,
	}, log)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown so in-flight requests finish on Ctrl-C or systemd stop.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", *addr, "mode", modeName(*mock), "version", version)
		if *devOrigin != "" {
			log.Warn("CORS enabled for development origin", "origin", *devOrigin)
		}
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

// readTokenFile loads the shared ingest token, refusing a world-readable file and
// a token short enough to be guessable.
//
// A secret read by a non-root service is normally root-owned and group-readable
// by a dedicated single-member group: the service can read it but cannot rewrite
// it, which is stricter than owning it 0600. So world access is the thing to
// refuse, not group access.
func readTokenFile(path string) (string, error) {
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

// modeName describes the data source in log output, matching what /healthz
// reports. These must agree: a log claiming "mock" on a live instance, or the
// reverse, is how someone ends up trusting generated numbers.
func modeName(mock bool) string {
	if mock {
		return "mock"
	}
	return "live"
}

// provisionUser creates or resets a user, reading the password from stdin.
//
// stdin rather than a flag: a password on the command line lands in shell history
// and in the process list, where any local user can read it.
func provisionUser(dsn, tenant, email, name, role string, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	fmt.Fprintf(os.Stderr, "Password for %s (minimum 12 characters): ", email)
	pw, err := readSecret()
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	again, err := readSecret()
	if err != nil {
		return err
	}
	if pw != again {
		return errors.New("passwords do not match")
	}

	if name == "" {
		name = email
	}
	store := auth.NewStore(pool)
	id, err := store.CreateUser(ctx, tenant, email, name, role, pw)
	if err != nil {
		return err
	}
	log.Info("user ready", "id", id, "email", email, "role", role, "tenant", tenant)
	fmt.Fprintln(os.Stderr, "Done. Sign in at /login.")
	return nil
}

// stdinReader is shared across prompts so buffered input survives between them.
var stdinReader = bufio.NewReader(os.Stdin)

// readSecret reads a line without echoing it when stdin is a terminal.
func readSecret() (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	// Piped input, for scripted provisioning. One shared reader: a fresh
	// bufio.Reader per prompt throws away whatever the previous one buffered, so
	// the second prompt would see EOF even though input remains.
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
