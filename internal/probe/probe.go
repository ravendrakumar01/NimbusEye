// Package probe runs the checks NimbusEye performs itself.
//
// This is the first component that measures something directly rather than
// reading a cloud provider's opinion. It answers one question per check — did it
// respond, and how fast — and writes the result as an availability status plus
// metric samples.
//
// It deliberately does not decide severity. The prober reports up or down; the
// alerter compares the metrics against threshold profiles and decides whether
// that is trouble or critical. Keeping that split means a threshold change takes
// effect without redeploying the prober, and there is exactly one place where
// "what counts as bad" is defined.
package probe

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	"nimbuseye/internal/model"
)

// Result is the outcome of one check.
type Result struct {
	// Status is up, down, or unknown when the check could not be performed at
	// all — a missing capability is not the same as a failing target.
	Status  string
	Message string
	// Samples are metric values keyed by the catalog metric key.
	Samples map[string]float64
}

func up(samples map[string]float64) Result {
	return Result{Status: model.StatusUp, Samples: samples}
}

func down(msg string, samples map[string]float64) Result {
	if samples == nil {
		samples = map[string]float64{}
	}
	return Result{Status: model.StatusDown, Message: msg, Samples: samples}
}

func unknown(msg string) Result {
	return Result{Status: model.StatusUnknown, Message: msg, Samples: map[string]float64{}}
}

// Config is a monitor's stored check configuration.
type Config struct {
	Target          string
	Method          string
	ExpectedStatus  []int
	MatchText       string
	FollowRedirects bool
	TimeoutSec      int
	Port            int
	RecordType      string
	Resolver        string
	ExpectedIP      string
}

// FromMap reads a Config out of the stored check_config JSON.
func FromMap(m map[string]any) Config {
	c := Config{FollowRedirects: true}
	s := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	i := func(k string) int {
		switch v := m[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
		return 0
	}
	c.Target = s("target")
	c.Method = s("method")
	c.MatchText = s("match_text")
	c.RecordType = s("record_type")
	c.Resolver = s("resolver")
	c.ExpectedIP = s("expected_ip")
	c.TimeoutSec = i("timeout_sec")
	c.Port = i("port")
	if v, ok := m["follow_redirects"].(bool); ok {
		c.FollowRedirects = v
	}
	if arr, ok := m["expected_status"].([]any); ok {
		for _, x := range arr {
			if f, ok := x.(float64); ok {
				c.ExpectedStatus = append(c.ExpectedStatus, int(f))
			}
		}
	}
	return c
}

func (c Config) timeout() time.Duration {
	if c.TimeoutSec <= 0 {
		return 10 * time.Second
	}
	return time.Duration(c.TimeoutSec) * time.Second
}

// Run dispatches to the check for a monitor type.
func Run(ctx context.Context, resourceType string, cfg Config, lastSeen *time.Time, expectEvery int) Result {
	switch resourceType {
	case "WEB_HTTP", "WEB_REST_API":
		return checkHTTP(ctx, cfg)
	case "WEB_PORT":
		return checkPort(ctx, cfg)
	case "WEB_DNS":
		return checkDNS(ctx, cfg)
	case "WEB_SSL_CERT":
		return checkSSL(ctx, cfg)
	case "WEB_DOMAIN_EXPIRY":
		return checkDomainExpiry(ctx, cfg)
	case "WEB_PING":
		return checkPing(ctx, cfg)
	case "WEB_HEARTBEAT":
		return checkHeartbeat(lastSeen, expectEvery)
	default:
		return unknown("no check implemented for " + resourceType)
	}
}

/* ------------------------------------------------------------------ HTTP */

// checkHTTP fetches a URL and records the phase timings.
//
// httptrace is used rather than timing the whole request, because "slow" has
// several causes and they need different fixes: DNS, TLS handshake and
// server-side latency are separate numbers here.
func checkHTTP(ctx context.Context, cfg Config) Result {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()

	method := cfg.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.Target, nil)
	if err != nil {
		return down("invalid request: "+err.Error(), nil)
	}
	req.Header.Set("User-Agent", "NimbusEye/1.0 (+monitoring)")

	var dnsStart, dnsDone, tlsStart, tlsDone, firstByte time.Time
	trace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:              func(httptrace.DNSDoneInfo) { dnsDone = time.Now() },
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { tlsDone = time.Now() },
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	client := &http.Client{
		Timeout: cfg.timeout(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			if cfg.FollowRedirects {
				return nil
			}
			return http.ErrUseLastResponse
		},
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		// A timeout and a refused connection are both "down", but the operator
		// needs to know which, so the underlying error text is preserved.
		return down(httpErrorMessage(err), map[string]float64{})
	}
	defer func() { _ = resp.Body.Close() }()

	// Read a bounded amount: enough to match against, not enough for a large
	// response to become a memory problem across thousands of checks.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	total := time.Since(start)

	samples := map[string]float64{
		"response_time": float64(total.Milliseconds()),
		"status_code":   float64(resp.StatusCode),
	}
	if !dnsStart.IsZero() && !dnsDone.IsZero() {
		samples["dns_time"] = float64(dnsDone.Sub(dnsStart).Milliseconds())
	}
	if !tlsStart.IsZero() && !tlsDone.IsZero() {
		samples["tls_handshake"] = float64(tlsDone.Sub(tlsStart).Milliseconds())
	}
	_ = firstByte

	if !statusAccepted(resp.StatusCode, cfg.ExpectedStatus) {
		want := "2xx or 3xx"
		if len(cfg.ExpectedStatus) > 0 {
			want = fmt.Sprint(cfg.ExpectedStatus)
		}
		return down(fmt.Sprintf("HTTP %d, expected %s", resp.StatusCode, want), samples)
	}
	if cfg.MatchText != "" && !strings.Contains(string(body), cfg.MatchText) {
		// The most useful check of the set: a page can return 200 while being
		// completely broken, and only content matching catches that.
		return down("response did not contain "+quote(cfg.MatchText), samples)
	}
	return up(samples)
}

func statusAccepted(code int, expected []int) bool {
	if len(expected) == 0 {
		return code >= 200 && code < 400
	}
	for _, e := range expected {
		if e == code {
			return true
		}
	}
	return false
}

func httpErrorMessage(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case strings.Contains(err.Error(), "no such host"):
		return "DNS lookup failed"
	case strings.Contains(err.Error(), "connection refused"):
		return "connection refused"
	case strings.Contains(err.Error(), "certificate"):
		return "TLS error: " + err.Error()
	default:
		return err.Error()
	}
}

func quote(s string) string {
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return "\"" + s + "\""
}

/* ------------------------------------------------------------------- TCP */

func checkPort(ctx context.Context, cfg Config) Result {
	addr := net.JoinHostPort(cfg.Target, fmt.Sprint(cfg.Port))
	d := net.Dialer{}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return down(httpErrorMessage(err), map[string]float64{})
	}
	elapsed := time.Since(start)
	_ = conn.Close()
	return up(map[string]float64{"connect_time": float64(elapsed.Milliseconds())})
}

/* ------------------------------------------------------------------- DNS */

func checkDNS(ctx context.Context, cfg Config) Result {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resolver := net.DefaultResolver
	if cfg.Resolver != "" {
		// Querying a specific resolver matters: "DNS works" from this host says
		// nothing about what the authoritative server is handing out.
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 5 * time.Second}
				return d.DialContext(ctx, network, net.JoinHostPort(cfg.Resolver, "53"))
			},
		}
	}

	rt := strings.ToUpper(cfg.RecordType)
	if rt == "" {
		rt = "A"
	}
	start := time.Now()
	var answers []string
	var err error

	switch rt {
	case "A", "AAAA":
		var ips []net.IP
		ips, err = resolver.LookupIP(ctx, map[string]string{"A": "ip4", "AAAA": "ip6"}[rt], cfg.Target)
		for _, ip := range ips {
			answers = append(answers, ip.String())
		}
	case "CNAME":
		var cname string
		cname, err = resolver.LookupCNAME(ctx, cfg.Target)
		if cname != "" {
			answers = append(answers, strings.TrimSuffix(cname, "."))
		}
	case "MX":
		var mx []*net.MX
		mx, err = resolver.LookupMX(ctx, cfg.Target)
		for _, m := range mx {
			answers = append(answers, strings.TrimSuffix(m.Host, "."))
		}
	case "TXT":
		answers, err = resolver.LookupTXT(ctx, cfg.Target)
	case "NS":
		var ns []*net.NS
		ns, err = resolver.LookupNS(ctx, cfg.Target)
		for _, n := range ns {
			answers = append(answers, strings.TrimSuffix(n.Host, "."))
		}
	default:
		return unknown("unsupported record type " + rt)
	}

	elapsed := time.Since(start)
	samples := map[string]float64{"resolve_time": float64(elapsed.Milliseconds())}

	if err != nil {
		return down("lookup failed: "+err.Error(), samples)
	}
	if len(answers) == 0 {
		return down("no "+rt+" record returned", samples)
	}
	if cfg.ExpectedIP != "" {
		for _, a := range answers {
			if a == cfg.ExpectedIP {
				return up(samples)
			}
		}
		return down(fmt.Sprintf("%s resolved to %s, expected %s",
			rt, strings.Join(answers, ", "), cfg.ExpectedIP), samples)
	}
	return up(samples)
}

/* ------------------------------------------------------------------- TLS */

func checkSSL(ctx context.Context, cfg Config) Result {
	port := cfg.Port
	if port == 0 {
		port = 443
	}
	addr := net.JoinHostPort(cfg.Target, fmt.Sprint(port))

	d := &net.Dialer{Timeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return down(httpErrorMessage(err), map[string]float64{})
	}
	defer func() { _ = raw.Close() }()

	// InsecureSkipVerify is set on purpose: the point is to inspect and report on
	// the certificate, including an expired or mismatched one. Refusing to
	// complete the handshake would make the check unable to tell us why.
	conn := tls.Client(raw, &tls.Config{ServerName: cfg.Target, InsecureSkipVerify: true}) //nolint:gosec
	if err := conn.HandshakeContext(ctx); err != nil {
		return down("TLS handshake failed: "+err.Error(), map[string]float64{})
	}
	defer func() { _ = conn.Close() }()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return down("server presented no certificate", map[string]float64{})
	}
	leaf := certs[0]
	days := time.Until(leaf.NotAfter).Hours() / 24
	samples := map[string]float64{"days_to_expiry": days}

	switch {
	case time.Now().After(leaf.NotAfter):
		return down(fmt.Sprintf("certificate expired on %s", leaf.NotAfter.Format("2 Jan 2006")), samples)
	case time.Now().Before(leaf.NotBefore):
		return down("certificate is not valid yet", samples)
	}
	// Hostname mismatch is reported but not treated as down: the certificate is
	// still valid, and the threshold profile for days_to_expiry is what this
	// monitor exists to watch.
	if err := leaf.VerifyHostname(cfg.Target); err != nil {
		return Result{Status: model.StatusUp, Samples: samples,
			Message: "certificate does not cover " + cfg.Target}
	}
	return up(samples)
}

/* --------------------------------------------------------- domain expiry */

// checkDomainExpiry reads the registration expiry over RDAP.
//
// RDAP rather than WHOIS: it returns JSON with a defined schema, where WHOIS is
// free text that differs per registry and needs a parser per TLD.
func checkDomainExpiry(ctx context.Context, cfg Config) Result {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://rdap.org/domain/"+cfg.Target, nil)
	if err != nil {
		return unknown("could not build RDAP request: " + err.Error())
	}
	req.Header.Set("Accept", "application/rdap+json")
	req.Header.Set("User-Agent", "NimbusEye/1.0 (+monitoring)")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		// The registry being unreachable says nothing about the domain, so this
		// is unknown rather than down. Reporting down here would page someone
		// about an RDAP outage.
		return unknown("RDAP lookup failed: " + err.Error())
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return down("domain not found in RDAP", map[string]float64{})
	}
	if resp.StatusCode/100 != 2 {
		return unknown(fmt.Sprintf("RDAP returned HTTP %d", resp.StatusCode))
	}

	var payload struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(body, &payload); err != nil {
		return unknown("could not parse RDAP response")
	}
	for _, e := range payload.Events {
		if e.Action != "expiration" {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.Date)
		if err != nil {
			continue
		}
		days := time.Until(t).Hours() / 24
		samples := map[string]float64{"days_to_expiry": days}
		if days < 0 {
			return down("domain registration expired on "+t.Format("2 Jan 2006"), samples)
		}
		return up(samples)
	}
	return unknown("RDAP response contained no expiration date")
}

/* ------------------------------------------------------------------ ICMP */

// checkPing is not implemented and says so.
//
// Unprivileged ICMP needs net.ipv4.ping_group_range to include the service user,
// and raw sockets need CAP_NET_RAW — which the systemd unit deliberately drops.
// Reporting a host as down because this process cannot open a socket would be a
// false alarm about someone else's infrastructure, so the check reports unknown
// with the reason instead.
func checkPing(context.Context, Config) Result {
	return unknown("ICMP checks are not enabled: the service runs without CAP_NET_RAW. " +
		"Use a Port check instead, or grant the capability.")
}

/* ------------------------------------------------------------- heartbeat */

// checkHeartbeat is an inbound check: staleness is the only thing to evaluate.
func checkHeartbeat(lastSeen *time.Time, expectEvery int) Result {
	if lastSeen == nil {
		return unknown("no heartbeat received yet")
	}
	age := time.Since(*lastSeen)
	samples := map[string]float64{"since_last_beat": age.Seconds()}
	if expectEvery > 0 && age > time.Duration(expectEvery)*time.Second {
		return down(fmt.Sprintf("no heartbeat for %s", age.Round(time.Second)), samples)
	}
	return up(samples)
}
