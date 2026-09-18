// Package demo builds an in-memory dataset that looks like a real multi-cloud
// estate, so the UI can be developed and load-tested before any cloud credential
// exists.
//
// Two properties matter here:
//
//   - Deterministic. Everything derives from one seed, so a restart produces the
//     identical estate. UI feedback is meaningless if the data reshuffles between
//     reloads.
//   - Realistic in shape, not just in volume. Most resources are healthy, alarms
//     cluster on a few resources, names follow environment conventions, and
//     regions are unevenly loaded. A uniform random estate hides exactly the
//     layout problems this data is meant to expose.
package demo

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/model"
	"nimbuseye/internal/store"
)

// Store is a read-mostly in-memory dataset. Acknowledging an alarm mutates it,
// hence the mutex.
type Store struct {
	mu        sync.RWMutex
	accounts  []model.CloudAccount
	resources []model.Resource
	byID      map[string]*model.Resource
	groups    []model.ResourceGroup
	alarms    []model.Alarm
	outages   []model.Outage
	tags      map[string][]string // tag key -> sorted distinct values
	// samples holds metric data delivered by a collector, keyed
	// "<native_id>|<metric_key>". Empty until a collector has run.
	samples map[string][]model.Sample
	seed    int64
}

var regions = map[string][]string{
	"oci":   {"ap-mumbai-1", "ap-hyderabad-1", "us-ashburn-1", "eu-frankfurt-1", "ap-singapore-1"},
	"aws":   {"ap-south-1", "ap-southeast-1", "us-east-1", "eu-west-1"},
	"azure": {"centralindia", "southindia", "eastus", "westeurope"},
	"gcp":   {"asia-south1", "us-central1", "europe-west1"},
	"k8s":   {"ap-south-1", "ap-mumbai-1", "centralindia"},
}

// Relative frequency of each category in a typical estate. Compute and storage
// dominate; this is what makes the resource list feel real rather than evenly
// sliced across 46 types.
var categoryWeight = map[string]int{
	"compute": 38, "storage": 22, "database": 12, "network": 10,
	"container": 8, "serverless": 6, "web": 4,
}

var (
	envs = []string{"prod", "prod", "prod", "stage", "uat", "dev"}
	apps = []string{"web", "api", "auth", "billing", "datalake", "etl", "reports",
		"gateway", "search", "cache", "queue", "polydata", "nhit", "crm", "portal"}
	owners      = []string{"platform-team", "app-team", "data-team", "network-team", "sre"}
	criticality = []string{"tier1", "tier1", "tier2", "tier2", "tier3"}
)

// Status mix, in parts per thousand. Roughly 94% healthy, which is what a real
// estate looks like; a dashboard designed against 50% failure looks wrong in
// production and hides the "everything is fine" case that users see most.
var statusMix = []struct {
	status string
	weight int
}{
	{model.StatusUp, 928},
	{model.StatusTrouble, 30},
	{model.StatusCritical, 14},
	{model.StatusDown, 10},
	{model.StatusMaintenance, 8},
	{model.StatusDiscovery, 6},
	{model.StatusSuspended, 3},
	{model.StatusUnknown, 1},
}

// New builds a dataset of n resources.
func New(seed int64, n int) *Store {
	r := rand.New(rand.NewPCG(uint64(seed), uint64(seed*2862933555777941757+3037000493)))
	s := &Store{
		byID: make(map[string]*model.Resource),
		tags: make(map[string][]string),
		seed: seed,
	}
	now := time.Now().UTC()

	s.buildAccounts(now)
	s.buildResources(r, now, n)
	s.buildGroups(r)
	s.buildAlarms(r, now)
	s.buildOutages(r, now)
	s.indexTags()
	s.rollUpAccounts()
	s.rollUpGroups()
	return s
}

func (s *Store) buildAccounts(now time.Time) {
	mk := func(provider, name, native string, minsAgo int, state, errMsg string,
		cfg map[string]string) model.CloudAccount {
		t := now.Add(-time.Duration(minsAgo) * time.Minute)
		return model.CloudAccount{
			ID: "acct-" + provider, Provider: provider, DisplayName: name,
			NativeAccountID: native, Regions: regions[provider], Enabled: true,
			LastDiscoveryAt: &t, DiscoveryState: state, LastError: errMsg,
			Config: cfg,
			// Illustrative paths only. Nothing reads these in mock mode, and no
			// real tenancy or key material appears anywhere in this dataset.
			CredentialsRef:       "/etc/nimbuseye/creds/" + provider + "-demo",
			DiscoveryIntervalSec: 3600,
			MetricIntervalSec:    300,
		}
	}
	s.accounts = []model.CloudAccount{
		mk("oci", "Demo Tenancy (Oracle Cloud)", "ocid1.tenancy.oc1..aaaaaaaaexampledemotenancy", 12, "ok", "",
			map[string]string{
				"tenancy_ocid": "ocid1.tenancy.oc1..aaaaaaaaexampledemotenancy",
				"user_ocid":    "ocid1.user.oc1..aaaaaaaaexampledemoreadonly",
				"fingerprint":  "00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff",
				"compartments": "",
			}),
		mk("aws", "Demo Account (AWS)", "000000000000", 8, "ok", "",
			map[string]string{"role_arn": "arn:aws:iam::000000000000:role/NimbusEyeReadOnly"}),
		mk("azure", "Demo Subscription (Azure)", "00000000-0000-0000-0000-000000000000", 21, "ok", "",
			map[string]string{
				"tenant_id": "00000000-0000-0000-0000-000000000000",
				"client_id": "00000000-0000-0000-0000-000000000001",
			}),
		// One deliberately degraded account. The UI must have something to render
		// for partial discovery, because a silently broken integration is the
		// most dangerous state a monitoring tool can be in.
		mk("gcp", "Demo Project (GCP)", "nimbuseye-demo-0001", 184, "partial",
			"cloudresourcemanager.googleapis.com: permission denied on 2 of 5 projects",
			map[string]string{"service_account_email": "nimbuseye-reader@nimbuseye-demo-0001.iam.gserviceaccount.com"}),
	}
}

func (s *Store) buildResources(r *rand.Rand, now time.Time, n int) {
	// Expand the catalog into a weighted pool so picking a type is one index.
	type entry struct{ t catalog.Type }
	var pool []catalog.Type
	for _, t := range catalog.All() {
		if t.Provider == "synthetic" {
			// Synthetic checks are user-created, not discovered, so they are
			// added separately in a smaller fixed number.
			continue
		}
		w := categoryWeight[t.Category]
		if w == 0 {
			w = 5
		}
		for i := 0; i < w; i++ {
			pool = append(pool, t)
		}
	}

	totalWeight := 0
	for _, m := range statusMix {
		totalWeight += m.weight
	}

	pickStatus := func() string {
		x := r.IntN(totalWeight)
		for _, m := range statusMix {
			if x < m.weight {
				return m.status
			}
			x -= m.weight
		}
		return model.StatusUp
	}

	seq := map[string]int{}
	for i := 0; i < n; i++ {
		t := pool[r.IntN(len(pool))]
		regs := regions[t.Provider]
		region := regs[r.IntN(len(regs))]
		env := envs[r.IntN(len(envs))]
		app := apps[r.IntN(len(apps))]

		short := strings.ToLower(t.Category)
		key := env + "-" + app + "-" + short
		seq[key]++
		name := fmt.Sprintf("%s-%s-%s-%02d", env, app, short, seq[key])

		status := pickStatus()
		// Non-production resources are less likely to be genuinely broken, and
		// dev resources are where suspended/unknown states really accumulate.
		if env == "dev" && model.IsUnhealthy(status) && r.IntN(2) == 0 {
			status = model.StatusUp
		}

		since := now.Add(-time.Duration(r.IntN(72*3600)) * time.Second)
		polled := now.Add(-time.Duration(r.IntN(300)) * time.Second)
		var polledPtr *time.Time
		if status != model.StatusSuspended && status != model.StatusDiscovery {
			polledPtr = &polled
		}

		avail := 99.5 + r.Float64()*0.5
		switch status {
		case model.StatusDown:
			avail = 80 + r.Float64()*15
		case model.StatusCritical:
			avail = 92 + r.Float64()*6
		case model.StatusTrouble:
			avail = 96 + r.Float64()*3
		case model.StatusSuspended, model.StatusDiscovery:
			avail = 0
		}

		res := model.Resource{
			ID:             fmt.Sprintf("res-%05d", i+1),
			CloudAccountID: "acct-" + t.Provider,
			Provider:       t.Provider,
			ResourceType:   t.Code,
			TypeName:       t.DisplayName,
			Category:       t.Category,
			NativeID:       nativeID(t.Provider, t.Code, region, i+1),
			DisplayName:    name,
			Region:         region,
			Status:         status,
			StatusSince:    since,
			LastPolledAt:   polledPtr,
			Suspended:      status == model.StatusSuspended,
			Tags: map[string]string{
				"env":         env,
				"application": app,
				"owner":       owners[r.IntN(len(owners))],
				"criticality": criticality[r.IntN(len(criticality))],
			},
			Attributes:      attributesFor(r, t),
			Availability24h: math.Round(avail*1000) / 1000,
		}
		s.resources = append(s.resources, res)
	}

	// A handful of synthetic checks, which behave differently: they are probed
	// by NimbusEye itself and have no cloud account.
	sites := []struct{ code, name, target string }{
		{"WEB_HTTP", "Corporate Website", "https://www.example.com"},
		{"WEB_HTTP", "Customer Portal", "https://portal.example.com"},
		{"WEB_REST_API", "Public API v2", "https://api.example.com/v2/health"},
		{"WEB_SSL_CERT", "portal.example.com SSL", "portal.example.com:443"},
		{"WEB_DOMAIN_EXPIRY", "example.com Domain", "example.com"},
		{"WEB_DNS", "Authoritative DNS", "ns1.example.com"},
		{"WEB_PING", "VPN Concentrator", "10.10.0.1"},
		{"WEB_PORT", "SMTP Relay", "smtp.example.com:587"},
	}
	for i, sc := range sites {
		t, ok := catalog.Get(sc.code)
		if !ok {
			continue
		}
		status := model.StatusUp
		if i == 3 {
			status = model.StatusTrouble // SSL nearing expiry
		}
		polled := now.Add(-time.Duration(r.IntN(60)) * time.Second)
		s.resources = append(s.resources, model.Resource{
			ID:              fmt.Sprintf("res-web-%02d", i+1),
			Provider:        "synthetic",
			ResourceType:    t.Code,
			TypeName:        t.DisplayName,
			Category:        t.Category,
			NativeID:        sc.target,
			DisplayName:     sc.name,
			Region:          "global",
			Status:          status,
			StatusSince:     now.Add(-time.Duration(r.IntN(48*3600)) * time.Second),
			LastPolledAt:    &polled,
			Tags:            map[string]string{"env": "prod", "application": "public"},
			Attributes:      map[string]any{"target": sc.target, "check_locations": []string{"ap-south-1", "ap-southeast-1", "eu-west-1"}},
			Availability24h: 99.8 + r.Float64()*0.2,
		})
	}

	for i := range s.resources {
		s.byID[s.resources[i].ID] = &s.resources[i]
	}
}

func nativeID(provider, code, region string, n int) string {
	switch provider {
	case "oci":
		kind := strings.ToLower(strings.TrimPrefix(code, "OCI_"))
		return fmt.Sprintf("ocid1.%s.oc1.%s.aaaaaaaa%011d", strings.ReplaceAll(kind, "_", ""), region, n)
	case "aws":
		return fmt.Sprintf("arn:aws:%s:%s:417238291045:resource/r-%08d",
			strings.ToLower(strings.SplitN(strings.TrimPrefix(code, "AWS_"), "_", 2)[0]), region, n)
	case "azure":
		return fmt.Sprintf("/subscriptions/b7f3a19c-2d84-4e5f-9a11-6c8d2e4f7b03/resourceGroups/rg-%s/providers/%s/r%06d",
			region, strings.TrimPrefix(code, "AZURE_"), n)
	case "gcp":
		return fmt.Sprintf("//nimbus-analytics-2481/%s/%s/r-%06d", region, strings.ToLower(strings.TrimPrefix(code, "GCP_")), n)
	default:
		return fmt.Sprintf("%s-%06d", strings.ToLower(code), n)
	}
}

func attributesFor(r *rand.Rand, t catalog.Type) map[string]any {
	a := map[string]any{}
	switch t.Category {
	case "compute":
		shapes := []string{"VM.Standard.E4.Flex", "t3.large", "m6i.xlarge", "Standard_D4s_v5", "e2-standard-4"}
		a["shape"] = shapes[r.IntN(len(shapes))]
		a["ocpus"] = 1 << r.IntN(4)
		a["memory_gb"] = (1 << r.IntN(4)) * 4
		a["os"] = []string{"Oracle Linux 8", "Ubuntu 22.04", "Ubuntu 24.04", "Windows Server 2022", "RHEL 9"}[r.IntN(5)]
	case "storage":
		a["size_gb"] = (r.IntN(40) + 1) * 50
		a["tier"] = []string{"Standard", "Infrequent Access", "Archive"}[r.IntN(3)]
		a["encrypted"] = true
	case "database":
		a["engine"] = []string{"PostgreSQL 16", "MySQL 8.0", "Oracle 19c", "SQL Server 2022"}[r.IntN(4)]
		a["storage_gb"] = (r.IntN(20) + 1) * 100
		a["multi_az"] = r.IntN(2) == 1
	case "container":
		a["k8s_version"] = []string{"1.28", "1.29", "1.30", "1.31"}[r.IntN(4)]
		a["node_count"] = r.IntN(12) + 2
	case "network":
		a["scheme"] = []string{"internet-facing", "internal"}[r.IntN(2)]
	case "serverless":
		a["runtime"] = []string{"python3.12", "nodejs20.x", "java21", "go1.x"}[r.IntN(4)]
		a["memory_mb"] = []int{128, 256, 512, 1024, 2048}[r.IntN(5)]
	}
	return a
}

func (s *Store) buildGroups(r *rand.Rand) {
	defs := []struct{ id, name, desc, parent string }{
		{"grp-prod", "Production", "All production workloads", ""},
		{"grp-prod-web", "Production / Web Tier", "Public-facing web and API", "grp-prod"},
		{"grp-prod-data", "Production / Data Tier", "Databases and data lake", "grp-prod"},
		{"grp-nonprod", "Non-Production", "Stage, UAT and development", ""},
		{"grp-oci", "Oracle Cloud", "All Oracle Cloud resources", ""},
		{"grp-k8s", "Kubernetes Platform", "All clusters, nodes and workloads", ""},
		{"grp-public", "Public Endpoints", "Synthetic checks on customer-facing URLs", ""},
	}
	for _, d := range defs {
		s.groups = append(s.groups, model.ResourceGroup{
			ID: d.id, DisplayName: d.name, Description: d.desc, ParentGroupID: d.parent,
		})
	}

	// Membership by rule, which is how it has to work at this scale. Manual
	// membership lists rot within weeks when discovery adds resources daily.
	for i := range s.resources {
		res := &s.resources[i]
		var g []string
		if res.Tags["env"] == "prod" {
			g = append(g, "grp-prod")
			switch res.Category {
			case "network", "web", "serverless":
				g = append(g, "grp-prod-web")
			case "database", "storage":
				g = append(g, "grp-prod-data")
			}
		} else if res.Provider != "synthetic" {
			g = append(g, "grp-nonprod")
		}
		if res.Provider == "oci" {
			g = append(g, "grp-oci")
		}
		if res.Provider == "k8s" || res.Category == "container" {
			g = append(g, "grp-k8s")
		}
		if res.Provider == "synthetic" {
			g = append(g, "grp-public")
		}
		res.GroupIDs = g
	}
}

func (s *Store) buildAlarms(r *rand.Rand, now time.Time) {
	users := []string{"ravendra.kumar", "raghav.kumar", "sre-oncall"}
	id := 0
	nextID := func() string { id++; return fmt.Sprintf("alm-%05d", id) }

	for i := range s.resources {
		res := &s.resources[i]
		if !model.IsUnhealthy(res.Status) {
			continue
		}
		t, ok := catalog.Get(res.ResourceType)
		if !ok {
			continue
		}

		// The availability alarm, when the resource is down.
		if res.Status == model.StatusDown {
			a := model.Alarm{
				ID: nextID(), ResourceID: res.ID, ResourceName: res.DisplayName,
				ResourceType: res.ResourceType, Provider: res.Provider, Region: res.Region,
				DedupKey: res.ID + ":availability", Severity: model.SeverityDown,
				State: model.AlarmOpen, Message: "Resource is not responding to availability checks",
				PollCount: 2 + r.IntN(4), OpenedAt: res.StatusSince,
			}
			if r.IntN(3) == 0 {
				ack := res.StatusSince.Add(time.Duration(r.IntN(1800)) * time.Second)
				a.State, a.AckedAt, a.AckedBy = model.AlarmAcknowledged, &ack, users[r.IntN(len(users))]
			}
			// Long-running unacknowledged alarms escalate.
			if a.State == model.AlarmOpen && now.Sub(a.OpenedAt) > time.Hour {
				a.EscalationLvl = 1 + r.IntN(2)
			}
			s.alarms = append(s.alarms, a)
			res.OpenAlarms++
		}

		// Metric alarms, on metrics that actually have thresholds defined.
		var candidates []catalog.Metric
		for _, m := range t.Metrics {
			if m.Trouble != nil || m.Critical != nil {
				candidates = append(candidates, m)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		count := 1
		if res.Status == model.StatusCritical && len(candidates) > 1 && r.IntN(2) == 0 {
			count = 2
		}
		perm := r.Perm(len(candidates))
		for k := 0; k < count && k < len(perm); k++ {
			m := candidates[perm[k]]
			sev := model.SeverityTrouble
			thr := m.Trouble
			if res.Status == model.StatusCritical && m.Critical != nil {
				sev, thr = model.SeverityCritical, m.Critical
			}
			if thr == nil {
				thr = m.Critical
				sev = model.SeverityCritical
			}
			if thr == nil {
				continue
			}
			obs := breach(r, *thr, m.HigherIsWorse)
			a := model.Alarm{
				ID: nextID(), ResourceID: res.ID, ResourceName: res.DisplayName,
				ResourceType: res.ResourceType, Provider: res.Provider, Region: res.Region,
				DedupKey: res.ID + ":" + m.Key, Severity: sev, State: model.AlarmOpen,
				MetricKey: m.Key, MetricLabel: m.Label, Unit: m.Unit,
				ObservedValue: &obs, ThresholdValue: thr,
				Message:   fmt.Sprintf("%s %s threshold (%s)", m.Label, direction(m.HigherIsWorse), formatVal(*thr, m.Unit)),
				PollCount: 3 + r.IntN(5),
				OpenedAt:  now.Add(-time.Duration(r.IntN(6*3600)+300) * time.Second),
			}
			if r.IntN(4) == 0 {
				ack := a.OpenedAt.Add(time.Duration(r.IntN(3600)) * time.Second)
				a.State, a.AckedAt, a.AckedBy = model.AlarmAcknowledged, &ack, users[r.IntN(len(users))]
			}
			s.alarms = append(s.alarms, a)
			res.OpenAlarms++
		}
	}

	// Resolved history, so the Alarms page has a past and MTTR is computable.
	for i := 0; i < 220; i++ {
		res := &s.resources[r.IntN(len(s.resources))]
		opened := now.Add(-time.Duration(r.IntN(30*24*3600)+3600) * time.Second)
		dur := time.Duration(r.IntN(4*3600)+120) * time.Second
		resolved := opened.Add(dur)
		if resolved.After(now) {
			continue
		}
		sevs := []string{model.SeverityTrouble, model.SeverityTrouble, model.SeverityCritical, model.SeverityDown}
		s.alarms = append(s.alarms, model.Alarm{
			ID: nextID(), ResourceID: res.ID, ResourceName: res.DisplayName,
			ResourceType: res.ResourceType, Provider: res.Provider, Region: res.Region,
			DedupKey: res.ID + ":historic", Severity: sevs[r.IntN(len(sevs))],
			State: model.AlarmResolved, Message: "Condition cleared",
			PollCount: 2 + r.IntN(3), OpenedAt: opened, ResolvedAt: &resolved,
		})
	}

	sort.Slice(s.alarms, func(i, j int) bool {
		return s.alarms[i].OpenedAt.After(s.alarms[j].OpenedAt)
	})
}

func breach(r *rand.Rand, threshold float64, higherIsWorse bool) float64 {
	if higherIsWorse {
		v := threshold * (1.05 + r.Float64()*0.35)
		if threshold <= 1 {
			v = threshold + float64(1+r.IntN(9))
		}
		return math.Round(v*100) / 100
	}
	v := threshold * (0.35 + r.Float64()*0.55)
	return math.Round(v*100) / 100
}

func direction(higherIsWorse bool) string {
	if higherIsWorse {
		return "above"
	}
	return "below"
}

func formatVal(v float64, unit string) string {
	switch unit {
	case "percent":
		return fmt.Sprintf("%.0f%%", v)
	case "bytes":
		return humanBytes(v)
	case "milliseconds":
		return fmt.Sprintf("%.0f ms", v)
	case "seconds":
		return fmt.Sprintf("%.3g s", v)
	default:
		return fmt.Sprintf("%.4g", v)
	}
}

func humanBytes(v float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func (s *Store) buildOutages(r *rand.Rand, now time.Time) {
	id := 0
	for i := 0; i < 140; i++ {
		res := &s.resources[r.IntN(len(s.resources))]
		start := now.Add(-time.Duration(r.IntN(30*24*3600)+600) * time.Second)
		dur := time.Duration(r.IntN(3*3600)+60) * time.Second
		end := start.Add(dur)
		if end.After(now) {
			continue
		}
		id++
		class := "outage"
		if r.IntN(8) == 0 {
			class = "maintenance"
		} else if r.IntN(20) == 0 {
			class = "false_positive"
		}
		causes := []string{
			"Instance stopped by autoscaling policy",
			"Availability domain networking event",
			"Backend health check failures",
			"Disk pressure caused kubelet eviction",
			"Provider API throttling during metric collection",
			"",
		}
		s.outages = append(s.outages, model.Outage{
			ID: fmt.Sprintf("out-%05d", id), ResourceID: res.ID, ResourceName: res.DisplayName,
			Provider: res.Provider, StartedAt: start, EndedAt: &end,
			DurationSec:  int(dur.Seconds()),
			Severity:     []string{model.SeverityDown, model.SeverityCritical}[r.IntN(2)],
			ClassifiedAs: class, RootCause: causes[r.IntN(len(causes))],
		})
	}
	// Ongoing outages for every currently-down resource.
	for i := range s.resources {
		res := &s.resources[i]
		if res.Status != model.StatusDown {
			continue
		}
		id++
		s.outages = append(s.outages, model.Outage{
			ID: fmt.Sprintf("out-%05d", id), ResourceID: res.ID, ResourceName: res.DisplayName,
			Provider: res.Provider, StartedAt: res.StatusSince, EndedAt: nil,
			DurationSec: int(now.Sub(res.StatusSince).Seconds()),
			Severity:    model.SeverityDown, ClassifiedAs: "outage",
		})
	}
	sort.Slice(s.outages, func(i, j int) bool {
		return s.outages[i].StartedAt.After(s.outages[j].StartedAt)
	})
}

func (s *Store) indexTags() {
	set := map[string]map[string]bool{}
	for _, r := range s.resources {
		for k, v := range r.Tags {
			if set[k] == nil {
				set[k] = map[string]bool{}
			}
			set[k][v] = true
		}
	}
	for k, vals := range set {
		var list []string
		for v := range vals {
			list = append(list, v)
		}
		sort.Strings(list)
		s.tags[k] = list
	}
}

func (s *Store) rollUpAccounts() {
	counts := map[string]int{}
	for _, r := range s.resources {
		if r.DeletedAt != nil {
			continue
		}
		counts[r.CloudAccountID]++
	}
	for i := range s.accounts {
		s.accounts[i].ResourceCount = counts[s.accounts[i].ID]
	}
}

func (s *Store) rollUpGroups() {
	total := map[string]int{}
	unhealthy := map[string]int{}
	worst := map[string]string{}
	for _, r := range s.resources {
		if r.DeletedAt != nil {
			continue
		}
		for _, g := range r.GroupIDs {
			total[g]++
			if model.IsUnhealthy(r.Status) {
				unhealthy[g]++
			}
			if cur, ok := worst[g]; !ok || model.StatusRank[r.Status] < model.StatusRank[cur] {
				worst[g] = r.Status
			}
		}
	}
	for i := range s.groups {
		id := s.groups[i].ID
		s.groups[i].ResourceCount = total[id]
		s.groups[i].Unhealthy = unhealthy[id]
		if w, ok := worst[id]; ok {
			s.groups[i].Status = w
		} else {
			s.groups[i].Status = model.StatusUnknown
		}
	}
}

// ---------------------------------------------------------------------------
// Query API
// ---------------------------------------------------------------------------

// The filter and page types are shared with the PostgreSQL backend so a query
// cannot mean one thing in mock mode and another in production.
type ResourceFilter = store.ResourceFilter

// AlarmFilter is an alias for the shared type.
type AlarmFilter = store.AlarmFilter

// Resources applies the filter and returns one page plus counts over the whole
// filtered set.
func (s *Store) Resources(f ResourceFilter) model.ResourceList {
	s.mu.RLock()
	defer s.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(f.Query))
	var tagK, tagV string
	if f.Tag != "" {
		if k, v, ok := strings.Cut(f.Tag, "="); ok {
			tagK, tagV = k, v
		}
	}

	var out []model.Resource
	for _, r := range s.resources {
		// Soft-deleted resources are retained for history but must not appear in
		// any list or count; showing them would inflate every figure in the UI
		// with infrastructure that no longer exists.
		if r.DeletedAt != nil {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.DisplayName), q) &&
			!strings.Contains(strings.ToLower(r.NativeID), q) &&
			!strings.Contains(strings.ToLower(r.TypeName), q) {
			continue
		}
		if !inList(f.Status, r.Status) || !inList(f.Provider, r.Provider) ||
			!inList(f.Type, r.ResourceType) || !inList(f.Category, r.Category) ||
			!inList(f.Region, r.Region) {
			continue
		}
		if f.GroupID != "" && !contains(r.GroupIDs, f.GroupID) {
			continue
		}
		if tagK != "" && r.Tags[tagK] != tagV {
			continue
		}
		if f.OnlyIssues && !model.IsUnhealthy(r.Status) {
			continue
		}
		out = append(out, r)
	}

	switch f.Sort {
	case "name":
		sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	case "availability":
		sort.Slice(out, func(i, j int) bool { return out[i].Availability24h < out[j].Availability24h })
	case "polled":
		sort.Slice(out, func(i, j int) bool {
			return derefTime(out[i].LastPolledAt).After(derefTime(out[j].LastPolledAt))
		})
	default: // status: worst first, then name — the order an operator wants
		sort.Slice(out, func(i, j int) bool {
			ri, rj := model.StatusRank[out[i].Status], model.StatusRank[out[j].Status]
			if ri != rj {
				return ri < rj
			}
			return out[i].DisplayName < out[j].DisplayName
		})
	}

	counts := model.ResourceCounts{ByStatus: map[string]int{}, Total: len(out)}
	var availSum float64
	var availN int
	for _, r := range out {
		counts.ByStatus[r.Status]++
		switch r.Status {
		case model.StatusMaintenance:
			counts.Maintenance++
		case model.StatusDiscovery:
			counts.Discovery++
		case model.StatusSuspended:
			counts.Suspended++
		case model.StatusUnknown:
			// An unknown state after a successful poll cycle almost always means
			// the monitor is misconfigured rather than the resource being down.
			counts.ConfigErrors++
		}
		counts.OpenAlarms += r.OpenAlarms
		if r.Availability24h > 0 {
			availSum += r.Availability24h
			availN++
		}
	}
	if availN > 0 {
		counts.Availability = math.Round(availSum/float64(availN)*1000) / 1000
	}

	page := store.Paginate(out, f.Page, f.PageSize)
	return model.ResourceList{
		Items: page.Items, Total: page.Total, Page: page.Page,
		PageSize: page.PageSize, Counts: counts,
	}
}

// Resource returns one resource by id.
func (s *Store) Resource(id string) (model.Resource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.byID[id]
	if !ok {
		return model.Resource{}, false
	}
	return *r, true
}

// Summary computes the Home dashboard payload in one pass.
func (s *Store) Summary() model.StatusSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sum := model.StatusSummary{
		ByStatus:    map[string]int{},
		ByProvider:  map[string]map[string]int{},
		ByCategory:  map[string]map[string]int{},
		OpenAlarms:  map[string]int{},
		GeneratedAt: time.Now().UTC(),
	}
	var availTotal float64
	var availCount int
	for _, r := range s.resources {
		if r.DeletedAt != nil {
			continue
		}
		sum.Total++
		sum.ByStatus[r.Status]++
		if sum.ByProvider[r.Provider] == nil {
			sum.ByProvider[r.Provider] = map[string]int{}
		}
		sum.ByProvider[r.Provider][r.Status]++
		if sum.ByCategory[r.Category] == nil {
			sum.ByCategory[r.Category] = map[string]int{}
		}
		sum.ByCategory[r.Category][r.Status]++
		if r.Availability24h > 0 {
			availTotal += r.Availability24h
			availCount++
		}
	}
	for _, a := range s.alarms {
		if a.State == model.AlarmOpen || a.State == model.AlarmAcknowledged {
			sum.OpenAlarms[a.Severity]++
			if a.State == model.AlarmOpen {
				sum.Unacked++
			}
		}
	}
	for _, o := range s.outages {
		if o.EndedAt == nil {
			sum.OngoingOutages++
		}
	}
	if availCount > 0 {
		sum.Availability = math.Round(availTotal/float64(availCount)*1000) / 1000
	}
	return sum
}

// Alarms returns one page of alarms, newest first.
func (s *Store) Alarms(f AlarmFilter) model.Page[model.Alarm] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q := strings.ToLower(strings.TrimSpace(f.Query))
	var out []model.Alarm
	for _, a := range s.alarms {
		if !inList(f.State, a.State) || !inList(f.Severity, a.Severity) ||
			!inList(f.Provider, a.Provider) {
			continue
		}
		if f.ResourceID != "" && a.ResourceID != f.ResourceID {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(a.ResourceName), q) &&
			!strings.Contains(strings.ToLower(a.Message), q) {
			continue
		}
		out = append(out, a)
	}
	return store.Paginate(out, f.Page, f.PageSize)
}

// Acknowledge marks an alarm as acknowledged. Returns false if not found or if
// the alarm is not in an acknowledgeable state.
func (s *Store) Acknowledge(id, user string) (model.Alarm, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.alarms {
		if s.alarms[i].ID != id {
			continue
		}
		if s.alarms[i].State != model.AlarmOpen {
			return s.alarms[i], false
		}
		now := time.Now().UTC()
		s.alarms[i].State = model.AlarmAcknowledged
		s.alarms[i].AckedAt = &now
		s.alarms[i].AckedBy = user
		return s.alarms[i], true
	}
	return model.Alarm{}, false
}

// Outages returns outage history, newest first. ongoing=true limits to open ones.
func (s *Store) Outages(resourceID string, ongoing bool, page, pageSize int) model.Page[model.Outage] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []model.Outage
	for _, o := range s.outages {
		if resourceID != "" && o.ResourceID != resourceID {
			continue
		}
		if ongoing && o.EndedAt != nil {
			continue
		}
		out = append(out, o)
	}
	return store.Paginate(out, page, pageSize)
}

// Accounts returns the connected cloud accounts.
func (s *Store) Accounts() []model.CloudAccount {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.CloudAccount(nil), s.accounts...)
}

// Groups returns the resource groups with their health rolled up.
func (s *Store) Groups() []model.ResourceGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.ResourceGroup(nil), s.groups...)
}

// Tags returns tag keys mapped to their distinct values, for filter dropdowns.
func (s *Store) Tags() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]string, len(s.tags))
	for k, v := range s.tags {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// Regions returns the distinct regions in use, grouped by provider.
func (s *Store) Regions() map[string][]string {
	out := map[string][]string{}
	for p, r := range regions {
		out[p] = append([]string(nil), r...)
	}
	return out
}

// Metrics synthesises a plausible series for one resource metric. The shape is
// derived from a hash of resource id and metric key, so a chart looks the same
// on every reload and across API restarts.
func (s *Store) Metrics(resourceID, metricKey string, from, to time.Time, points int) (model.MetricSeries, error) {
	s.mu.RLock()
	res, ok := s.byID[resourceID]
	var openMetric bool
	var breachVal float64
	if ok {
		for _, a := range s.alarms {
			if a.ResourceID == resourceID && a.MetricKey == metricKey &&
				(a.State == model.AlarmOpen || a.State == model.AlarmAcknowledged) &&
				a.ObservedValue != nil {
				openMetric, breachVal = true, *a.ObservedValue
				break
			}
		}
	}
	s.mu.RUnlock()

	if !ok {
		return model.MetricSeries{}, fmt.Errorf("resource %q not found", resourceID)
	}
	t, _ := catalog.Get(res.ResourceType)
	m, found := t.Metric(metricKey)
	if !found {
		return model.MetricSeries{}, fmt.Errorf("metric %q not defined on %s", metricKey, res.ResourceType)
	}
	if points <= 0 || points > 2000 {
		points = 288
	}

	// If a collector has delivered real samples for this series, serve those.
	// Falling back to generated data for a live resource would mean showing
	// invented numbers for real infrastructure, which is worse than showing none.
	if real := s.realSeries(res.NativeID, metricKey, from, to); len(real) > 0 {
		return model.MetricSeries{
			ResourceID: resourceID, MetricKey: m.Key, Label: m.Label,
			Unit: m.Unit, Trouble: m.Trouble, Critical: m.Critical,
			Samples: real,
		}, nil
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(resourceID + "|" + metricKey))
	seed := h.Sum64()
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))

	base, amp := baseline(m, r)
	step := to.Sub(from) / time.Duration(points)
	series := model.MetricSeries{
		ResourceID: resourceID, MetricKey: m.Key, Label: m.Label,
		Unit: m.Unit, Trouble: m.Trouble, Critical: m.Critical,
		Samples: make([]model.Sample, 0, points),
	}
	for i := 0; i < points; i++ {
		ts := from.Add(step * time.Duration(i))
		// Daily seasonality plus a slower weekly drift plus noise.
		hour := float64(ts.Hour()) + float64(ts.Minute())/60
		daily := math.Sin((hour - 6) / 24 * 2 * math.Pi)
		drift := math.Sin(float64(ts.Unix())/(86400*3.5)*2*math.Pi) * 0.25
		noise := (r.Float64() - 0.5) * 0.18
		v := base + amp*(daily*0.6+drift+noise)

		// If an alarm is open on this metric, converge the tail onto the
		// breaching value so the chart and the alarm agree.
		if openMetric {
			progress := float64(i) / float64(points-1)
			if progress > 0.72 {
				w := (progress - 0.72) / 0.28
				v = v*(1-w) + breachVal*w
			}
		}
		if m.Unit == "percent" {
			v = math.Max(0, math.Min(100, v))
		} else {
			v = math.Max(0, v)
		}
		series.Samples = append(series.Samples, model.Sample{
			T: ts.UTC(), V: math.Round(v*1000) / 1000,
		})
	}
	return series, nil
}

func baseline(m catalog.Metric, r *rand.Rand) (base, amp float64) {
	switch m.Unit {
	case "percent":
		base = 25 + r.Float64()*35
		return base, base * 0.35
	case "milliseconds":
		base = 80 + r.Float64()*400
		return base, base * 0.4
	case "seconds":
		base = 0.01 + r.Float64()*0.05
		return base, base * 0.5
	case "bytes":
		base = float64(int64(1)<<uint(28+r.IntN(6))) * (0.5 + r.Float64())
		return base, base * 0.08
	case "bytes_per_sec":
		base = 1e6 + r.Float64()*8e7
		return base, base * 0.45
	case "ops_per_sec":
		base = 5 + r.Float64()*400
		return base, base * 0.5
	default: // count
		base = 1 + r.Float64()*40
		return base, base * 0.5
	}
}

// ---------------------------------------------------------------------------

func inList(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	return contains(list, v)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
