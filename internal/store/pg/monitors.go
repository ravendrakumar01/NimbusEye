package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"nimbuseye/internal/model"
	"nimbuseye/internal/store"
)

// This file implements user-created ("synthetic") monitors: the checks NimbusEye
// performs itself, as opposed to resources discovered from a cloud account.
//
// They are stored in the same `resources` table with cloud_account_id NULL, so
// every list, filter, count, alarm and report treats them identically. The
// alternative — a separate table for synthetic monitors — would mean duplicating
// all of that, and the two would drift.

// CreateMonitor adds a user-created monitor.
func (s *Store) CreateMonitor(in store.MonitorInput) (model.Resource, error) {
	if err := store.ValidateMonitor(in); err != nil {
		return model.Resource{}, err
	}
	cfg := store.BuildCheckConfig(in)
	interval := store.DefaultInterval(in.ResourceType, in.CheckIntervalSec)
	nativeID := store.NewSyntheticNativeID()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var id string
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		// status starts as 'discovery', meaning "configured but not yet checked".
		// Starting at 'up' would show a green monitor that has never run.
		if err := tx.QueryRow(ctx,
			`INSERT INTO resources
			   (tenant_id, cloud_account_id, resource_type, native_id, display_name,
			    region, status, status_since, attributes, check_config,
			    check_interval_sec, discovered_at, last_seen_at)
			 VALUES (current_tenant_id(), NULL, $1, $2, $3, 'global', 'discovery', now(),
			         '{}'::jsonb, $4, $5, now(), now())
			 RETURNING id::text`,
			in.ResourceType, nativeID, strings.TrimSpace(in.DisplayName), cfg, interval).
			Scan(&id); err != nil {
			return err
		}
		return s.replaceUserTags(ctx, tx, id, in.Tags)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return model.Resource{}, &store.ValidationError{Fields: map[string]string{
				"display_name": "A monitor with this name already exists.",
			}}
		}
		return model.Resource{}, err
	}

	res, _ := s.Resource(id)
	s.log.Info("monitor created", "id", id, "type", in.ResourceType, "name", in.DisplayName)
	return res, nil
}

// UpdateMonitor edits a user-created monitor. Cloud-discovered resources are
// rejected: their fields come from the provider and would be overwritten on the
// next discovery run anyway.
func (s *Store) UpdateMonitor(id string, in store.MonitorInput) (model.Resource, error) {
	current, ok := s.Resource(id)
	if !ok {
		return model.Resource{}, store.ErrResourceNotFound
	}
	if current.CloudAccountID != "" {
		return model.Resource{}, &store.ValidationError{Fields: map[string]string{
			"resource_type": "This resource is discovered from a cloud account and cannot be edited here.",
		}}
	}
	// The type is fixed once created: changing it would invalidate the stored
	// history, which is recorded against a type's metric definitions.
	in.ResourceType = current.ResourceType
	if err := store.ValidateMonitor(in); err != nil {
		return model.Resource{}, err
	}

	cfg := store.BuildCheckConfig(in)
	interval := store.DefaultInterval(in.ResourceType, in.CheckIntervalSec)
	nativeID := store.NewSyntheticNativeID()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE resources SET display_name = $2, native_id = $3, check_config = $4,
			        check_interval_sec = $5, updated_at = now()
			 WHERE id = $1::uuid AND cloud_account_id IS NULL AND deleted_at IS NULL`,
			id, strings.TrimSpace(in.DisplayName), nativeID, cfg, interval)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrResourceNotFound
		}
		return s.replaceUserTags(ctx, tx, id, in.Tags)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return model.Resource{}, &store.ValidationError{Fields: map[string]string{
				"display_name": "Another monitor already has this name.",
			}}
		}
		return model.Resource{}, err
	}
	res, _ := s.Resource(id)
	return res, nil
}

// DeleteMonitor removes a user-created monitor.
//
// Soft-deleted, not erased: its outages and availability history stay queryable
// for reports covering the period it existed. The partial unique index excludes
// deleted rows, so the same target can be added again afterwards.
func (s *Store) DeleteMonitor(id string) error {
	current, ok := s.Resource(id)
	if !ok {
		return store.ErrResourceNotFound
	}
	if current.CloudAccountID != "" {
		return fmt.Errorf("resource is discovered from a cloud account; remove the account instead")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE resources SET deleted_at = now(), updated_at = now()
			 WHERE id = $1::uuid AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrResourceNotFound
		}
		// Close anything still open, or the dashboard keeps counting an alarm for
		// a monitor that no longer exists.
		if _, err := tx.Exec(ctx,
			`UPDATE alerts SET state = 'resolved', resolved_at = now(), updated_at = now()
			 WHERE resource_id = $1::uuid AND state IN ('open','acknowledged')`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE outages SET ended_at = now(),
			        duration_sec = extract(epoch FROM (now() - started_at))::int
			 WHERE resource_id = $1::uuid AND ended_at IS NULL`, id)
		return err
	})
}

// SetSuspended activates or suspends a monitor. A suspended monitor is neither
// checked nor alerted on, but keeps its history.
func (s *Store) SetSuspended(id string, suspended bool) (model.Resource, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := s.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE resources
			 SET suspended = $2,
			     status = CASE WHEN $2 THEN 'suspended'
			                   ELSE 'discovery' END,
			     status_since = now(), updated_at = now()
			 WHERE id = $1::uuid AND deleted_at IS NULL`, id, suspended)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrResourceNotFound
		}
		if suspended {
			// Suspending is an explicit "stop telling me about this", so open
			// alarms are cleared rather than left to linger.
			if _, err := tx.Exec(ctx,
				`UPDATE alerts SET state = 'resolved', resolved_at = now(), updated_at = now()
				 WHERE resource_id = $1::uuid AND state IN ('open','acknowledged')`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE outages SET ended_at = now(),
				        duration_sec = extract(epoch FROM (now() - started_at))::int
				 WHERE resource_id = $1::uuid AND ended_at IS NULL`, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.Resource{}, err
	}
	res, _ := s.Resource(id)
	return res, nil
}

// replaceUserTags reconciles the user-supplied tags on a resource, leaving
// cloud-imported tags alone.
func (s *Store) replaceUserTags(ctx context.Context, tx pgx.Tx, resourceID string, tags map[string]string) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM resource_tags rt USING tags tg
		 WHERE rt.tag_id = tg.id AND rt.resource_id = $1::uuid AND tg.source = 'user'`,
		resourceID); err != nil {
		return err
	}
	for k, v := range tags {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			continue
		}
		var tagID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO tags (tenant_id, key, value, source)
			 VALUES (current_tenant_id(), $1, $2, 'user')
			 ON CONFLICT (tenant_id, key, value, source) DO UPDATE SET key = EXCLUDED.key
			 RETURNING id::text`, k, v).Scan(&tagID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO resource_tags (tag_id, resource_id) VALUES ($1::uuid, $2::uuid)
			 ON CONFLICT DO NOTHING`, tagID, resourceID); err != nil {
			return err
		}
	}
	return nil
}
