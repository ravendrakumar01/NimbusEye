package store

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/url"
	"strings"

	"nimbuseye/internal/catalog"
)

// MonitorInput is what the Add Monitor form submits.
//
// Only synthetic monitor types can be created this way. Cloud resources are
// discovered, never declared: letting someone hand-create an "EC2 instance"
// record would produce a monitor that looks real and measures nothing.
type MonitorInput struct {
	ResourceType     string            `json:"resource_type"`
	DisplayName      string            `json:"display_name"`
	Target           string            `json:"target"`
	CheckIntervalSec int               `json:"check_interval_sec"`
	Tags             map[string]string `json:"tags"`

	// HTTP and REST API options.
	Method          string `json:"method"`
	ExpectedStatus  []int  `json:"expected_status"`
	MatchText       string `json:"match_text"`
	FollowRedirects *bool  `json:"follow_redirects"`
	TimeoutSec      int    `json:"timeout_sec"`

	// Port check.
	Port int `json:"port"`

	// DNS check.
	RecordType string `json:"record_type"`
	Resolver   string `json:"resolver"`
	ExpectedIP string `json:"expected_ip"`
}

// CreatableTypes are the monitor types a user may add directly: everything
// NimbusEye can measure itself, without a cloud credential or an agent.
var CreatableTypes = []string{
	"WEB_HTTP", "WEB_REST_API", "WEB_PING", "WEB_PORT",
	"WEB_DNS", "WEB_SSL_CERT", "WEB_DOMAIN_EXPIRY", "WEB_HEARTBEAT",
}

// IsCreatable reports whether a type can be created from the UI.
func IsCreatable(code string) bool {
	for _, c := range CreatableTypes {
		if c == code {
			return true
		}
	}
	return false
}

// MonitorFields describes the inputs each creatable type needs, so the Add
// Monitor form is generated from the backend rather than hardcoded per type.
func MonitorFields(code string) []FieldSpec {
	target := func(label, placeholder, help string) FieldSpec {
		return FieldSpec{Key: "target", Label: label, Placeholder: placeholder, Help: help, Required: true}
	}
	interval := FieldSpec{
		Key: "check_interval_sec", Label: "Check Interval (seconds)", Placeholder: "60",
		Help: "How often to run this check. Minimum 30 seconds.",
	}

	switch code {
	case "WEB_HTTP":
		return []FieldSpec{
			target("Website URL", "https://www.example.com", "Must include http:// or https://"),
			{Key: "method", Label: "HTTP Method", Placeholder: "GET"},
			{Key: "expected_status", Label: "Expected Status Codes", Placeholder: "200,301",
				Help: "Comma-separated. Blank accepts any 2xx or 3xx response."},
			{Key: "match_text", Label: "Response Must Contain", Placeholder: "optional",
				Help: "Fails the check if this text is absent, even on a 200. Catches pages that return OK while broken."},
			{Key: "timeout_sec", Label: "Timeout (seconds)", Placeholder: "10"},
			interval,
		}
	case "WEB_REST_API":
		return []FieldSpec{
			target("Endpoint URL", "https://api.example.com/v1/health", "Must include http:// or https://"),
			{Key: "method", Label: "HTTP Method", Placeholder: "GET"},
			{Key: "expected_status", Label: "Expected Status Codes", Placeholder: "200"},
			{Key: "match_text", Label: "Response Must Contain", Placeholder: `"status":"ok"`},
			{Key: "timeout_sec", Label: "Timeout (seconds)", Placeholder: "10"},
			interval,
		}
	case "WEB_PING":
		return []FieldSpec{
			target("Hostname or IP", "10.10.0.1", "ICMP echo. Many cloud networks block ICMP by default."),
			interval,
		}
	case "WEB_PORT":
		return []FieldSpec{
			target("Hostname", "smtp.example.com", "TCP connect check."),
			{Key: "port", Label: "Port", Placeholder: "587", Required: true},
			{Key: "timeout_sec", Label: "Timeout (seconds)", Placeholder: "10"},
			interval,
		}
	case "WEB_DNS":
		return []FieldSpec{
			target("Domain to resolve", "example.com", ""),
			{Key: "record_type", Label: "Record Type", Placeholder: "A"},
			{Key: "resolver", Label: "Resolver", Placeholder: "1.1.1.1",
				Help: "Blank uses the server's own resolver."},
			{Key: "expected_ip", Label: "Expected Answer", Placeholder: "optional",
				Help: "Fails if the record does not resolve to this value. Detects hijacking and stale records."},
			interval,
		}
	case "WEB_SSL_CERT":
		return []FieldSpec{
			target("Host", "portal.example.com", "Port 443 is assumed unless one is given."),
			{Key: "port", Label: "Port", Placeholder: "443"},
			interval,
		}
	case "WEB_DOMAIN_EXPIRY":
		return []FieldSpec{
			target("Domain", "example.com", "Checked over WHOIS/RDAP once a day."),
		}
	case "WEB_HEARTBEAT":
		return []FieldSpec{
			{Key: "target", Label: "Heartbeat Name", Placeholder: "nightly-backup", Required: true,
				Help: "An inbound check: your job calls NimbusEye. Missing a call raises the alarm."},
			{Key: "check_interval_sec", Label: "Expected Every (seconds)", Placeholder: "86400",
				Help: "Alerts when no heartbeat arrives within this window."},
		}
	}
	return nil
}

// ValidateMonitor checks an Add Monitor submission.
func ValidateMonitor(in MonitorInput) error {
	f := map[string]string{}

	t, known := catalog.Get(in.ResourceType)
	switch {
	case !known:
		f["resource_type"] = "Unknown monitor type."
	case !IsCreatable(in.ResourceType):
		// Being explicit here rather than silently accepting it: a hand-created
		// cloud resource would appear in every count while measuring nothing.
		f["resource_type"] = t.DisplayName + " monitors are discovered from a connected cloud account, not created here."
	}

	if strings.TrimSpace(in.DisplayName) == "" {
		f["display_name"] = "Required."
	} else if len(in.DisplayName) > 200 {
		f["display_name"] = "Must be 200 characters or fewer."
	}

	target := strings.TrimSpace(in.Target)
	if target == "" {
		f["target"] = "Required."
	} else {
		switch in.ResourceType {
		case "WEB_HTTP", "WEB_REST_API":
			u, err := url.Parse(target)
			switch {
			case err != nil || u.Host == "":
				f["target"] = "Must be a full URL, e.g. https://www.example.com"
			case u.Scheme != "http" && u.Scheme != "https":
				f["target"] = "Only http:// and https:// are supported."
			}
		case "WEB_PING", "WEB_PORT", "WEB_SSL_CERT":
			if strings.Contains(target, "/") || strings.Contains(target, " ") {
				f["target"] = "Enter a hostname or IP address, without a scheme or path."
			}
		case "WEB_DNS", "WEB_DOMAIN_EXPIRY":
			if !strings.Contains(target, ".") || strings.Contains(target, "/") {
				f["target"] = "Enter a domain name, e.g. example.com"
			}
		case "WEB_HEARTBEAT":
			if strings.ContainsAny(target, " /?&#") {
				f["target"] = "Use letters, digits and hyphens only."
			}
		}
	}

	if in.ResourceType == "WEB_PORT" {
		if in.Port < 1 || in.Port > 65535 {
			f["port"] = "Must be between 1 and 65535."
		}
	}
	if in.ResourceType == "WEB_SSL_CERT" && in.Port != 0 && (in.Port < 1 || in.Port > 65535) {
		f["port"] = "Must be between 1 and 65535."
	}

	if in.Method != "" {
		switch strings.ToUpper(in.Method) {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		default:
			f["method"] = "Unsupported HTTP method."
		}
	}
	for _, c := range in.ExpectedStatus {
		if c < 100 || c > 599 {
			f["expected_status"] = "Status codes must be between 100 and 599."
			break
		}
	}
	if in.TimeoutSec != 0 && (in.TimeoutSec < 1 || in.TimeoutSec > 120) {
		f["timeout_sec"] = "Must be between 1 and 120 seconds."
	}
	if in.CheckIntervalSec != 0 && (in.CheckIntervalSec < 30 || in.CheckIntervalSec > 86400) {
		f["check_interval_sec"] = "Must be between 30 seconds and 24 hours."
	}
	if in.Resolver != "" && net.ParseIP(in.Resolver) == nil {
		f["resolver"] = "Must be an IP address."
	}
	if in.ExpectedIP != "" && net.ParseIP(in.ExpectedIP) == nil {
		f["expected_ip"] = "Must be an IP address."
	}
	if in.RecordType != "" {
		switch strings.ToUpper(in.RecordType) {
		case "A", "AAAA", "CNAME", "MX", "TXT", "NS":
		default:
			f["record_type"] = "Supported types are A, AAAA, CNAME, MX, TXT and NS."
		}
	}

	// Refuse targets that would make the prober scan the host it runs on or the
	// cloud metadata service. A monitoring tool that will connect to any address
	// on request is a server-side request forgery primitive.
	if reason := blockedTarget(in.ResourceType, target); reason != "" {
		f["target"] = reason
	}

	if len(f) > 0 {
		return &ValidationError{Fields: f}
	}
	return nil
}

// blockedTarget rejects loopback, link-local and metadata addresses.
func blockedTarget(code, target string) string {
	if code == "WEB_HEARTBEAT" || target == "" {
		return ""
	}
	host := target
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		host = u.Hostname()
	}
	host = strings.Trim(host, "[]")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsLoopback():
			return "Loopback addresses cannot be monitored from here."
		case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
			return "Link-local addresses cannot be monitored."
		case ip.IsUnspecified():
			return "Not a routable address."
		}
		// 169.254.169.254 is the cloud instance metadata endpoint on every major
		// provider; reaching it would expose this server's own credentials.
		if ip.String() == "169.254.169.254" {
			return "The instance metadata address cannot be monitored."
		}
	}
	switch strings.ToLower(host) {
	case "localhost", "metadata.google.internal", "metadata":
		return "This host cannot be monitored from here."
	}
	return ""
}

// BuildCheckConfig normalises an input into the stored check_config, applying
// defaults so the prober never has to guess.
func BuildCheckConfig(in MonitorInput) map[string]any {
	cfg := map[string]any{"target": strings.TrimSpace(in.Target)}

	switch in.ResourceType {
	case "WEB_HTTP", "WEB_REST_API":
		method := strings.ToUpper(strings.TrimSpace(in.Method))
		if method == "" {
			method = "GET"
		}
		cfg["method"] = method
		if len(in.ExpectedStatus) > 0 {
			cfg["expected_status"] = in.ExpectedStatus
		}
		if s := strings.TrimSpace(in.MatchText); s != "" {
			cfg["match_text"] = s
		}
		cfg["follow_redirects"] = in.FollowRedirects == nil || *in.FollowRedirects
		cfg["timeout_sec"] = orDefaultInt(in.TimeoutSec, 10)
	case "WEB_PORT":
		cfg["port"] = in.Port
		cfg["timeout_sec"] = orDefaultInt(in.TimeoutSec, 10)
	case "WEB_SSL_CERT":
		cfg["port"] = orDefaultInt(in.Port, 443)
	case "WEB_DNS":
		rt := strings.ToUpper(strings.TrimSpace(in.RecordType))
		if rt == "" {
			rt = "A"
		}
		cfg["record_type"] = rt
		if in.Resolver != "" {
			cfg["resolver"] = in.Resolver
		}
		if in.ExpectedIP != "" {
			cfg["expected_ip"] = in.ExpectedIP
		}
	case "WEB_PING":
		cfg["timeout_sec"] = orDefaultInt(in.TimeoutSec, 5)
	}
	return cfg
}

// DefaultInterval returns the check interval to use when none was given.
func DefaultInterval(code string, requested int) int {
	if requested >= 30 {
		return requested
	}
	if t, ok := catalog.Get(code); ok && t.DefaultPollSec >= 30 {
		return t.DefaultPollSec
	}
	return 300
}

// NewSyntheticNativeID generates the opaque identifier for a user-created
// monitor.
//
// Random rather than derived from the target: several checks against one URL are
// legitimate — one asserting a status code, another asserting page content — and
// a target-derived id makes the second one impossible. Uniqueness that users
// actually want is on the monitor's name, which the database enforces.
//
// It is also stable across edits, so changing a monitor's URL does not orphan its
// metric history.
func NewSyntheticNativeID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not recoverable and not worth papering over with
		// a weaker source; a duplicate id would silently merge two monitors'
		// metric history.
		panic("store: cannot generate monitor id: " + err.Error())
	}
	return "syn-" + hex.EncodeToString(b)
}

func orDefaultInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
