package store

import (
	"context"
	"time"
)

// Administration types and contract.
//
// Like Reporter, this is a separate interface implemented only by the PostgreSQL
// backend. Administration mutates configuration that the alerter and collector
// read; a mock that accepted writes and dropped them would be actively misleading
// during UI work.
//
// Two rules run through everything here:
//
//   Secrets are referenced, never stored. A webhook URL with a token in it, an API
//   key, an SMTP password: all live in a server-side file and the database holds
//   only the path. The same rule already governs cloud credentials, and applying
//   it inconsistently is how a database dump ends up containing keys.
//
//   The catalog is the authority on what a metric means. A threshold rule cannot
//   name a metric the resource type does not have, and cannot declare its own
//   direction — whether high or low is bad is a property of the metric, not a
//   choice an operator makes per profile.

// ThresholdRule is one metric's alerting configuration inside a profile.
type ThresholdRule struct {
	Metric     string   `json:"metric"`
	Op         string   `json:"op"`
	Trouble    *float64 `json:"trouble"`
	Critical   *float64 `json:"critical"`
	PollsCheck int      `json:"polls_check"`
	Strategy   string   `json:"strategy"`

	// The following are filled from the catalog on read and ignored on write.
	// They exist so the UI can label and validate a rule without a second lookup,
	// and so nobody can persist a direction that contradicts the metric.
	Label         string `json:"label,omitempty"`
	Unit          string `json:"unit,omitempty"`
	HigherIsWorse bool   `json:"higher_is_worse"`
}

// ThresholdProfile binds a set of rules to a resource type.
type ThresholdProfile struct {
	ID           string `json:"id"`
	DisplayName  string `json:"display_name"`
	ResourceType string `json:"resource_type"`
	TypeName     string `json:"type_name"`
	Provider     string `json:"provider"`

	Rules          []ThresholdRule `json:"rules"`
	DownPollsCheck int             `json:"down_polls_check"`

	SystemGenerated bool      `json:"system_generated"`
	IsDefault       bool      `json:"is_default"`
	UpdatedAt       time.Time `json:"updated_at"`

	// ResourceCount is how many live monitors this profile governs. Editing a
	// threshold that covers 56 volumes should not feel the same as editing one
	// that covers nothing, so the number is on the screen.
	ResourceCount int `json:"resource_count"`

	// AvailableMetrics lists every metric the type defines, so the editor can
	// offer what is not yet configured rather than expecting it to be typed.
	AvailableMetrics []ThresholdRule `json:"available_metrics,omitempty"`
}

// ThresholdProfileUpdate is the writable part of a profile.
type ThresholdProfileUpdate struct {
	Rules          []ThresholdRule `json:"rules"`
	DownPollsCheck *int            `json:"down_polls_check"`
}

// AlertRule routes one severity to a set of channels.
type AlertRule struct {
	Severity string   `json:"severity"`
	Channels []string `json:"channels"`
}

// EscalationLevel re-notifies, through different channels, after a delay.
type EscalationLevel struct {
	Level        int      `json:"level"`
	AfterMinutes int      `json:"after_minutes"`
	Channels     []string `json:"channels"`
}

// NotificationProfile is the routing policy: who is told, through what, after how
// long, and what happens when nobody responds.
type NotificationProfile struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`

	NotificationDelay int `json:"notification_delay"`

	BusinessHoursID            *string `json:"business_hours_id"`
	BusinessHoursName          string  `json:"business_hours_name,omitempty"`
	NotifyOutsideBusinessHours bool    `json:"notify_outside_business_hours"`

	AlertRules       []AlertRule       `json:"alert_rules"`
	EscalationLevels []EscalationLevel `json:"escalation_levels"`

	PersistentAlertInterval int  `json:"persistent_alert_interval"`
	NotifyOnRecovery        bool `json:"notify_on_recovery"`
	RCANeeded               bool `json:"rca_needed"`
	IsDefault               bool `json:"is_default"`

	UpdatedAt time.Time `json:"updated_at"`
}

// NotificationProfileUpdate is the writable part of a notification profile.
type NotificationProfileUpdate struct {
	DisplayName             *string            `json:"display_name"`
	NotificationDelay       *int               `json:"notification_delay"`
	NotifyOnRecovery        *bool              `json:"notify_on_recovery"`
	PersistentAlertInterval *int               `json:"persistent_alert_interval"`
	RCANeeded               *bool              `json:"rca_needed"`
	AlertRules              *[]AlertRule       `json:"alert_rules"`
	EscalationLevels        *[]EscalationLevel `json:"escalation_levels"`
}

// NotificationChannel is one delivery destination.
type NotificationChannel struct {
	ID          string `json:"id"`
	ChannelType string `json:"channel_type"`
	DisplayName string `json:"display_name"`

	// Config carries only non-secret settings: an address, a room name, a method.
	Config map[string]any `json:"config"`
	// SecretRef is a server-side path. The value behind it is never read by the
	// API and never returned.
	SecretRef  string     `json:"secret_ref,omitempty"`
	Enabled    bool       `json:"enabled"`
	VerifiedAt *time.Time `json:"verified_at"`
	CreatedAt  time.Time  `json:"created_at"`

	// UsedBy counts the notification profiles referencing this channel, so
	// deleting one that is in use can warn rather than silently break routing.
	UsedBy int `json:"used_by"`
}

// ChannelInput creates or updates a channel.
type ChannelInput struct {
	ChannelType string         `json:"channel_type"`
	DisplayName string         `json:"display_name"`
	Config      map[string]any `json:"config"`
	SecretRef   string         `json:"secret_ref"`
	Enabled     *bool          `json:"enabled"`
}

// AdminUser is a user as administration sees them. There is no password field in
// either direction: hashes are never read out, and a password is never set here.
type AdminUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	MFAEnabled  bool   `json:"mfa_enabled"`
	Timezone    string `json:"timezone,omitempty"`

	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`

	// HasPassword distinguishes an invited user who has never set one from an
	// active user, which is otherwise invisible and confusing.
	HasPassword bool `json:"has_password"`
	// LockedUntil is surfaced because "why can this person not log in" is a
	// support question, and the answer is usually here.
	LockedUntil  *time.Time `json:"locked_until"`
	FailedLogins int        `json:"failed_logins"`
}

// UserInput invites a user or edits one.
type UserInput struct {
	Email       string  `json:"email"`
	DisplayName string  `json:"display_name"`
	Role        string  `json:"role"`
	Status      *string `json:"status"`
	Timezone    string  `json:"timezone"`
}

// AuditEntry is one recorded action. Append-only: there is no update or delete.
type AuditEntry struct {
	ID         int64          `json:"id"`
	UserID     *string        `json:"user_id"`
	UserEmail  string         `json:"user_email,omitempty"`
	Action     string         `json:"action"`
	ObjectType string         `json:"object_type,omitempty"`
	ObjectID   string         `json:"object_id,omitempty"`
	Detail     map[string]any `json:"detail"`
	IP         string         `json:"ip,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// AuditFilter narrows the audit log.
type AuditFilter struct {
	Action string
	UserID string
	Since  *time.Time
	Limit  int
	Before int64
}

// AuditPage is a page of audit entries with a cursor.
type AuditPage struct {
	Entries []AuditEntry `json:"entries"`
	// NextBefore is the cursor for the following page, 0 when there is no more.
	// A keyset cursor rather than an offset because the log grows while it is
	// being read, and an offset would skip or repeat rows.
	NextBefore int64 `json:"next_before"`
	// Actions lists the distinct action names present, for a filter control.
	Actions []string `json:"actions,omitempty"`
}

// Administrator is implemented by backends that support administration.
type Administrator interface {
	ThresholdProfiles(ctx context.Context, provider []string) ([]ThresholdProfile, error)
	ThresholdProfile(ctx context.Context, id string) (ThresholdProfile, error)
	UpdateThresholdProfile(ctx context.Context, id string, u ThresholdProfileUpdate) (ThresholdProfile, error)

	NotificationProfiles(ctx context.Context) ([]NotificationProfile, error)
	UpdateNotificationProfile(ctx context.Context, id string, u NotificationProfileUpdate) (NotificationProfile, error)

	NotificationChannels(ctx context.Context) ([]NotificationChannel, error)
	CreateChannel(ctx context.Context, in ChannelInput) (NotificationChannel, error)
	UpdateChannel(ctx context.Context, id string, in ChannelInput) (NotificationChannel, error)
	DeleteChannel(ctx context.Context, id string) error

	AdminUsers(ctx context.Context) ([]AdminUser, error)
	CreateUser(ctx context.Context, in UserInput) (AdminUser, error)
	UpdateUser(ctx context.Context, id string, in UserInput) (AdminUser, error)
	DeleteUser(ctx context.Context, id string) error
	UnlockUser(ctx context.Context, id string) error

	AuditLog(ctx context.Context, f AuditFilter) (AuditPage, error)

	// WriteAudit records an administrative action. It returns nothing: a failure
	// to record must not roll back a change the operator has already been told
	// succeeded, and the gap is itself visible in the log.
	WriteAudit(ctx context.Context, userID, action, objectType, objectID, ip string, detail map[string]any)
}
