package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nimbuseye/internal/model"
)

// Runner claims due checks, executes them and writes the results.
type Runner struct {
	pool     *pgxpool.Pool
	tenantID string
	log      *slog.Logger
	// Concurrency bound. Checks are almost entirely network wait, so this can be
	// high relative to CPU count; the ceiling exists so a thousand monitors do not
	// open a thousand sockets at once and trip a NAT or conntrack limit.
	concurrency int
}

// Report summarises one pass.
type Report struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Checked    int
	Up         int
	Down       int
	Unknown    int
	Changed    int
	Samples    int
}

// Open connects and resolves the tenant.
func Open(ctx context.Context, dsn, tenantSlug string, concurrency int, log *slog.Logger) (*Runner, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("probe: parse dsn: %w", err)
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("probe: connect: %w", err)
	}
	if tenantSlug == "" {
		tenantSlug = "default"
	}
	var tenantID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM tenant_by_slug($1)`, tenantSlug).
		Scan(&tenantID); err != nil {
		pool.Close()
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("probe: no active tenant %q", tenantSlug)
		}
		return nil, err
	}
	if concurrency <= 0 {
		concurrency = 20
	}
	return &Runner{pool: pool, tenantID: tenantID, log: log, concurrency: concurrency}, nil
}

// Close releases the pool.
func (r *Runner) Close() { r.pool.Close() }

func (r *Runner) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('nimbuseye.tenant_id', $1, true)`, r.tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type job struct {
	id           string
	nativeID     string
	resourceType string
	displayName  string
	status       string
	cfg          Config
	interval     int
	lastSeen     *time.Time
}

// Run executes every check that is due.
func (r *Runner) Run(ctx context.Context) (Report, error) {
	rep := Report{StartedAt: time.Now().UTC()}

	var jobs []job
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id::text, native_id, resource_type, display_name, status,
			        check_config, coalesce(check_interval_sec, 300), last_check_at
			 FROM resources
			 WHERE cloud_account_id IS NULL
			   AND deleted_at IS NULL
			   AND NOT suspended
			   AND (last_check_at IS NULL
			        OR last_check_at < now() - make_interval(secs => coalesce(check_interval_sec, 300)))
			 ORDER BY last_check_at NULLS FIRST
			 LIMIT 500`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var j job
			var raw []byte
			if err := rows.Scan(&j.id, &j.nativeID, &j.resourceType, &j.displayName,
				&j.status, &raw, &j.interval, &j.lastSeen); err != nil {
				return err
			}
			var m map[string]any
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &m); err != nil {
					r.log.Warn("monitor has unreadable check_config",
						"monitor", j.displayName, "err", err)
					continue
				}
			}
			j.cfg = FromMap(m)
			jobs = append(jobs, j)
		}
		return rows.Err()
	})
	if err != nil {
		rep.FinishedAt = time.Now().UTC()
		return rep, err
	}
	if len(jobs) == 0 {
		rep.FinishedAt = time.Now().UTC()
		return rep, nil
	}

	type outcome struct {
		j   job
		res Result
	}
	results := make([]outcome, len(jobs))
	sem := make(chan struct{}, r.concurrency)
	var wg sync.WaitGroup

	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// A panic in one check must not take the whole pass down with it.
			defer func() {
				if v := recover(); v != nil {
					results[i] = outcome{j, unknown(fmt.Sprintf("check panicked: %v", v))}
				}
			}()
			results[i] = outcome{j, Run(ctx, j.resourceType, j.cfg, j.lastSeen, j.interval)}
		}(i, j)
	}
	wg.Wait()

	// Written in one transaction: a pass either records fully or not at all, so
	// the dashboard never shows half a sweep.
	err = r.withTx(ctx, func(tx pgx.Tx) error {
		for _, o := range results {
			if o.res.Status == "" {
				continue
			}
			rep.Checked++
			switch o.res.Status {
			case model.StatusUp:
				rep.Up++
			case model.StatusDown:
				rep.Down++
			default:
				rep.Unknown++
			}
			if o.res.Status != o.j.status {
				rep.Changed++
			}

			// status_since only moves on an actual change, so "down for 2 hours"
			// does not reset to zero every time the check runs.
			// No grace period here. A prober check is a direct measurement: the
			// request either completed or it did not, so its result is authoritative
			// in a way a cloud metric read is not.
			if _, err := tx.Exec(ctx,
				`UPDATE resources
				 SET status = $2,
				     status_since = CASE WHEN status <> $2 THEN now() ELSE status_since END,
				     confirmed_at = CASE WHEN $2 = 'up' THEN now() ELSE confirmed_at END,
				     last_check_at = now(), last_polled_at = now(),
				     last_poll_error = $3, updated_at = now()
				 WHERE id = $1::uuid`,
				o.j.id, o.res.Status, nullIfEmpty(o.res.Message)); err != nil {
				return err
			}

			for key, v := range o.res.Samples {
				if _, err := tx.Exec(ctx,
					`INSERT INTO metric_samples (tenant_id, native_id, metric_key, t, v)
					 VALUES (current_tenant_id(), $1, $2, now(), $3)
					 ON CONFLICT (tenant_id, native_id, metric_key, t) DO UPDATE SET v = EXCLUDED.v`,
					o.j.nativeID, key, v); err != nil {
					return err
				}
				rep.Samples++
			}

			if o.res.Status != o.j.status {
				r.log.Info("monitor status changed",
					"monitor", o.j.displayName, "from", o.j.status, "to", o.res.Status,
					"detail", o.res.Message)
			}
		}
		return nil
	})

	rep.FinishedAt = time.Now().UTC()
	return rep, err
}

// RecordHeartbeat marks an inbound heartbeat as received.
func (r *Runner) RecordHeartbeat(ctx context.Context, name string) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE resources SET last_seen_at = now(), updated_at = now()
			 WHERE resource_type = 'WEB_HEARTBEAT' AND cloud_account_id IS NULL
			   AND deleted_at IS NULL AND check_config->>'target' = $1`, name)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("no heartbeat monitor named %q", name)
		}
		return nil
	})
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
