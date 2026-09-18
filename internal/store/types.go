// Package store defines the contract every storage backend implements, plus the
// validation that must behave identically whichever backend is in use.
//
// Two implementations exist: internal/demo (in-memory, generated data, for local
// UI work) and internal/store/pg (PostgreSQL). Keeping the query shapes and the
// validation rules here means the two cannot drift into accepting different
// input, which is the usual way a "mock mode" stops predicting production.
package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"nimbuseye/internal/collect"
	"nimbuseye/internal/model"
)

// ResourceFilter describes a resource list query. Zero values mean "no filter".
type ResourceFilter struct {
	Query      string
	Status     []string
	Provider   []string
	Type       []string
	Category   []string
	Region     []string
	GroupID    string
	Tag        string // "key=value"
	OnlyIssues bool
	Sort       string // name | status | availability | polled
	Page       int
	PageSize   int
}

// AlarmFilter describes an alarm list query.
type AlarmFilter struct {
	State      []string
	Severity   []string
	Provider   []string
	ResourceID string
	Query      string
	Page       int
	PageSize   int
}

// AccountInput is what the Admin form submits.
//
// Note what is absent: the private key itself. Only a server-side path to it is
// accepted (CredentialsRef). Tenancy, user and fingerprint values are not
// secrets — they identify a principal but cannot authenticate one — so they are
// ordinary configuration. The key never travels through the API and is never
// stored in the database.
type AccountInput struct {
	Provider             string            `json:"provider"`
	DisplayName          string            `json:"display_name"`
	NativeAccountID      string            `json:"native_account_id"`
	Regions              []string          `json:"regions"`
	CredentialsRef       string            `json:"credentials_ref"`
	Config               map[string]string `json:"config"`
	Enabled              *bool             `json:"enabled"`
	DiscoveryIntervalSec int               `json:"discovery_interval_sec"`
	MetricIntervalSec    int               `json:"metric_interval_sec"`
}

// ValidationError carries per-field messages so the form can mark the offending
// inputs instead of showing one opaque banner.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	return "invalid input: " + strings.Join(keys, ", ")
}

var (
	ErrAccountNotFound  = errors.New("cloud account not found")
	ErrResourceNotFound = errors.New("resource not found")
)

// FieldSpec describes one input the Admin form must collect.
type FieldSpec struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
	Help        string `json:"help"`
	Required    bool   `json:"required"`
	// IsPath marks a value that must be a server-side file path, because the
	// underlying secret must never be sent to the API.
	IsPath bool `json:"is_path"`
}

// Check is one step of a connectivity verification.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | failed | skipped
	Detail string `json:"detail,omitempty"`
}

// VerifyResult is the outcome of verifying an account's configuration.
type VerifyResult struct {
	OK      bool    `json:"ok"`
	Checks  []Check `json:"checks"`
	Message string  `json:"message"`
}

// ServiceTile is one cell of the per-account service grid.
type ServiceTile struct {
	ResourceType string `json:"resource_type"`
	DisplayName  string `json:"display_name"`
	Icon         string `json:"icon"`
	Category     string `json:"category"`
	Count        int    `json:"count"`
	Unhealthy    int    `json:"unhealthy"`
	Enabled      bool   `json:"enabled"`
}

// Store is the full surface the HTTP API needs.
type Store interface {
	Resources(ResourceFilter) model.ResourceList
	Resource(id string) (model.Resource, bool)
	Summary() model.StatusSummary
	Alarms(AlarmFilter) model.Page[model.Alarm]
	Acknowledge(id, user string) (model.Alarm, bool)
	Outages(resourceID string, ongoing bool, page, pageSize int) model.Page[model.Outage]
	Accounts() []model.CloudAccount
	Account(id string) (model.CloudAccount, bool)
	AddAccount(AccountInput) (model.CloudAccount, error)
	UpdateAccount(id string, in AccountInput) (model.CloudAccount, error)
	DeleteAccount(id string) (int, error)
	VerifyAccount(id string, stat func(string) (bool, bool, error)) (VerifyResult, error)
	ServiceView(accountID string) ([]ServiceTile, error)
	Groups() []model.ResourceGroup
	Tags() map[string][]string
	Regions() map[string][]string
	Metrics(resourceID, metricKey string, from, to time.Time, points int) (model.MetricSeries, error)
	// User-created (synthetic) monitors. Cloud resources are discovered, not
	// created, so these only accept types in CreatableTypes.
	CreateMonitor(MonitorInput) (model.Resource, error)
	UpdateMonitor(id string, in MonitorInput) (model.Resource, error)
	DeleteMonitor(id string) error
	SetSuspended(id string, suspended bool) (model.Resource, error)

	UpsertResources(accountID string, resources []model.Resource) (int, int, error)
	PutSamples(samples []collect.Sample) (int, error)
	RecordRun(run collect.RunReport) error
}

// ConfirmationGrace is how long an "up" status survives without a fresh metric.
//
// Cloud metric reads are not reliable per-run: the same resource returns a
// datapoint on one pass and nothing on the next, with nothing having changed. A
// run that finds no metric therefore is not evidence the resource stopped working,
// and demoting it immediately makes the status flap every collection interval.
//
// Sized at three default collection intervals (15 minutes each), so a resource has
// to be silent across three consecutive passes before its status changes. Beyond
// that the silence does mean something — usually that the monitoring plugin is not
// enabled — and "unknown" is the honest answer.
const ConfirmationGrace = 45 * time.Minute

// Closer is implemented by backends holding resources that need releasing.
type Closer interface {
	Close(ctx context.Context) error
}

/* ------------------------------------------------------- provider metadata */

// KnownRegions is the region list offered per provider in the Admin form.
//
// This is a starting set, not an exhaustive one: a region absent here can still
// be typed by an operator, and the collector does not consult this list.
var KnownRegions = map[string][]string{
	"oci":   {"ap-mumbai-1", "ap-hyderabad-1", "us-ashburn-1", "eu-frankfurt-1", "ap-singapore-1"},
	"aws":   {"ap-south-1", "ap-southeast-1", "us-east-1", "eu-west-1"},
	"azure": {"centralindia", "southindia", "eastus", "westeurope"},
	"gcp":   {"asia-south1", "us-central1", "europe-west1"},
	"k8s":   {"ap-south-1", "ap-mumbai-1", "centralindia"},
}

// ProviderFields describes, per provider, the non-secret configuration the Admin
// form must collect. The UI renders its form from this rather than hardcoding
// field lists, so adding a provider does not mean editing the frontend.
func ProviderFields(provider string) []FieldSpec {
	switch provider {
	case "oci":
		return []FieldSpec{
			{Key: "tenancy_ocid", Label: "Tenancy OCID", Placeholder: "ocid1.tenancy.oc1..aaaa…", Required: true,
				Help: "Found under Profile → Tenancy in the OCI console."},
			{Key: "user_ocid", Label: "User OCID", Placeholder: "ocid1.user.oc1..aaaa…", Required: true,
				Help: "A dedicated read-only user is strongly preferred over a human account."},
			{Key: "fingerprint", Label: "API Key Fingerprint", Placeholder: "aa:bb:cc:…", Required: true,
				Help: "Shown beside the uploaded public key in the OCI console."},
			{Key: "compartments", Label: "Compartment OCIDs", Placeholder: "leave blank to discover the whole tenancy",
				Help: "Comma-separated. Blank means recursive discovery from the tenancy root."},
		}
	case "aws":
		return []FieldSpec{
			{Key: "role_arn", Label: "Assume Role ARN", Placeholder: "arn:aws:iam::123456789012:role/NimbusEyeReadOnly", Required: true,
				Help: "Cross-account role with ReadOnlyAccess. Preferred over long-lived access keys."},
			{Key: "external_id", Label: "External ID", Placeholder: "optional but recommended",
				Help: "Protects the role against the confused-deputy problem."},
		}
	case "azure":
		return []FieldSpec{
			{Key: "tenant_id", Label: "Directory (tenant) ID", Placeholder: "00000000-0000-0000-0000-000000000000", Required: true},
			{Key: "client_id", Label: "Application (client) ID", Placeholder: "00000000-0000-0000-0000-000000000000", Required: true,
				Help: "Service principal with the Reader role on the subscription."},
		}
	case "gcp":
		return []FieldSpec{
			{Key: "service_account_email", Label: "Service Account", Placeholder: "nimbuseye-reader@project.iam.gserviceaccount.com", Required: true,
				Help: "Needs roles/viewer and roles/monitoring.viewer."},
		}
	}
	return nil
}

// CredentialFieldFor names the file the collector will read the secret from.
func CredentialFieldFor(provider string) FieldSpec {
	switch provider {
	case "oci":
		return FieldSpec{Key: "credentials_ref", Label: "Private Key File (server path)", IsPath: true, Required: true,
			Placeholder: "/etc/nimbuseye/creds/oci-prod.pem",
			Help:        "Path on the NimbusEye server, readable only by the service user. The key is never uploaded or stored in the database."}
	case "aws":
		return FieldSpec{Key: "credentials_ref", Label: "Credentials File (server path)", IsPath: true,
			Placeholder: "/etc/nimbuseye/creds/aws-prod.ini",
			Help:        "Optional when the server has an instance profile that can assume the role."}
	case "azure":
		return FieldSpec{Key: "credentials_ref", Label: "Client Secret File (server path)", IsPath: true, Required: true,
			Placeholder: "/etc/nimbuseye/creds/azure-prod.secret",
			Help:        "File containing only the client secret. Never sent through this form."}
	case "gcp":
		return FieldSpec{Key: "credentials_ref", Label: "Service Account JSON (server path)", IsPath: true, Required: true,
			Placeholder: "/etc/nimbuseye/creds/gcp-analytics.json",
			Help:        "Path to the downloaded service account key file on the server."}
	}
	return FieldSpec{Key: "credentials_ref", Label: "Credentials (server path)", IsPath: true}
}

var (
	ocidRe        = regexp.MustCompile(`^ocid1\.[a-z0-9]+\.oc[0-9]+\.[a-z0-9.-]*\.?[a-zA-Z0-9._-]+$`)
	awsAccountRe  = regexp.MustCompile(`^[0-9]{12}$`)
	guidRe        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	gcpProjectRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	fingerprintRe = regexp.MustCompile(`^([0-9a-f]{2}:){15}[0-9a-f]{2}$`)
)

// Validate checks an account submission. `existing` is used only for the
// duplicate check; `editingID` excludes the record being edited from it.
func Validate(in AccountInput, existing []model.CloudAccount, editingID string) error {
	f := map[string]string{}

	switch in.Provider {
	case "oci", "aws", "azure", "gcp":
	default:
		f["provider"] = "Must be one of oci, aws, azure or gcp."
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		f["display_name"] = "Required."
	}

	id := strings.TrimSpace(in.NativeAccountID)
	if id == "" {
		f["native_account_id"] = "Required."
	} else {
		switch in.Provider {
		case "oci":
			if !ocidRe.MatchString(id) {
				f["native_account_id"] = "Does not look like a tenancy OCID."
			}
		case "aws":
			if !awsAccountRe.MatchString(id) {
				f["native_account_id"] = "An AWS account id is 12 digits."
			}
		case "azure":
			if !guidRe.MatchString(id) {
				f["native_account_id"] = "A subscription id is a GUID."
			}
		case "gcp":
			if !gcpProjectRe.MatchString(id) {
				f["native_account_id"] = "A project id is 6-30 lowercase letters, digits or hyphens."
			}
		}
	}

	if len(in.Regions) == 0 {
		f["regions"] = "Select at least one region."
	}

	for _, spec := range ProviderFields(in.Provider) {
		v := strings.TrimSpace(in.Config[spec.Key])
		if spec.Required && v == "" {
			f[spec.Key] = "Required."
			continue
		}
		if v == "" {
			continue
		}
		switch spec.Key {
		case "tenancy_ocid", "user_ocid":
			if !ocidRe.MatchString(v) {
				f[spec.Key] = "Does not look like an OCID."
			}
		case "fingerprint":
			if !fingerprintRe.MatchString(strings.ToLower(v)) {
				f[spec.Key] = "Expected 16 colon-separated hex pairs."
			}
		case "tenant_id", "client_id":
			if !guidRe.MatchString(v) {
				f[spec.Key] = "Expected a GUID."
			}
		}
	}

	cred := CredentialFieldFor(in.Provider)
	ref := strings.TrimSpace(in.CredentialsRef)
	if cred.Required && ref == "" {
		f["credentials_ref"] = "Required."
	}
	if ref != "" {
		if !strings.HasPrefix(ref, "/") {
			f["credentials_ref"] = "Must be an absolute path on the server."
		}
		// Reject anything that looks like a pasted key rather than a path. This is
		// the single most likely user error here, and silently storing a private
		// key in the database is exactly what this design exists to prevent.
		if strings.Contains(ref, "BEGIN") || strings.Contains(ref, "\n") || len(ref) > 512 {
			f["credentials_ref"] = "Paste a file path, not the key itself."
		}
	}

	for _, a := range existing {
		if a.ID != editingID && a.Provider == in.Provider && a.NativeAccountID == id {
			f["native_account_id"] = "This account is already connected."
		}
	}

	if len(f) > 0 {
		return &ValidationError{Fields: f}
	}
	return nil
}

// BuildVerifyResult runs the checks that do not need a cloud API, given an
// account and a stat function. Shared so both backends report identically.
//
// The cloud handshake is reported as "skipped" rather than faked: a verification
// that claims success without ever talking to the provider is worse than none.
func BuildVerifyResult(acct model.CloudAccount, statCredential func(string) (bool, bool, error)) VerifyResult {
	res := VerifyResult{OK: true}
	add := func(name, status, detail string) {
		res.Checks = append(res.Checks, Check{Name: name, Status: status, Detail: detail})
		if status == "failed" {
			res.OK = false
		}
	}

	add("Account identifier format", "ok", acct.NativeAccountID)

	if len(acct.Regions) == 0 {
		add("Regions configured", "failed", "no regions selected")
	} else {
		add("Regions configured", "ok", strings.Join(acct.Regions, ", "))
	}

	switch {
	case acct.CredentialsRef == "":
		add("Credential file", "skipped", "no path configured")
	case statCredential == nil:
		add("Credential file", "skipped", "not checked in this mode")
	default:
		exists, tooOpen, err := statCredential(acct.CredentialsRef)
		switch {
		case err != nil:
			add("Credential file", "failed", err.Error())
		case !exists:
			add("Credential file", "failed", "not found at "+acct.CredentialsRef)
		case tooOpen:
			// A key readable by other local users is a real finding, not a nit.
			add("Credential file permissions", "failed",
				"file is world-accessible; run chmod 640 and chown root:nimbuseye")
		default:
			add("Credential file", "ok", "present, not world-accessible")
		}
	}

	add("Cloud API handshake", "skipped", "requires the collector")

	if res.OK {
		res.Message = "Configuration looks valid. The cloud API handshake still needs the collector."
	} else {
		res.Message = "Configuration has problems that would stop discovery."
	}
	return res
}

// Paginate clamps page parameters and slices a result set.
func Paginate[T any](items []T, page, pageSize int) model.Page[T] {
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	if page <= 0 {
		page = 1
	}
	total := len(items)
	start := min((page-1)*pageSize, total)
	end := min(start+pageSize, total)
	out := items[start:end]
	if out == nil {
		out = []T{}
	}
	return model.Page[T]{Items: out, Total: total, Page: page, PageSize: pageSize}
}
