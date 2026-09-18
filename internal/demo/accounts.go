package demo

import (
	"fmt"
	"strings"
	"time"

	"nimbuseye/internal/catalog"
	"nimbuseye/internal/model"
	"nimbuseye/internal/store"
)

// AddAccount registers a cloud account and puts it into the discovering state.
func (s *Store) AddAccount(in store.AccountInput) (model.CloudAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := store.Validate(in, s.accounts, ""); err != nil {
		return model.CloudAccount{}, err
	}

	if in.DiscoveryIntervalSec <= 0 {
		in.DiscoveryIntervalSec = 3600
	}
	if in.MetricIntervalSec <= 0 {
		in.MetricIntervalSec = 300
	}

	now := time.Now().UTC()
	acct := model.CloudAccount{
		ID:                   fmt.Sprintf("acct-%s-%d", in.Provider, len(s.accounts)+1),
		Provider:             in.Provider,
		DisplayName:          strings.TrimSpace(in.DisplayName),
		NativeAccountID:      strings.TrimSpace(in.NativeAccountID),
		Regions:              in.Regions,
		Enabled:              in.Enabled == nil || *in.Enabled,
		CredentialsRef:       strings.TrimSpace(in.CredentialsRef),
		Config:               in.Config,
		DiscoveryIntervalSec: in.DiscoveryIntervalSec,
		MetricIntervalSec:    in.MetricIntervalSec,
		// A freshly connected account has discovered nothing yet. Reporting it as
		// healthy would be a lie, and reporting zero resources without saying why
		// is what made the reference account confusing.
		DiscoveryState:  "running",
		LastDiscoveryAt: &now,
	}
	s.accounts = append(s.accounts, acct)
	return acct, nil
}

// UpdateAccount edits an existing account. A blank CredentialsRef leaves the
// stored path untouched rather than clearing it.
func (s *Store) UpdateAccount(id string, in store.AccountInput) (model.CloudAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i := range s.accounts {
		if s.accounts[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return model.CloudAccount{}, store.ErrAccountNotFound
	}

	if in.CredentialsRef == "" {
		in.CredentialsRef = s.accounts[idx].CredentialsRef
	}
	if err := store.Validate(in, s.accounts, id); err != nil {
		return model.CloudAccount{}, err
	}

	a := &s.accounts[idx]
	a.DisplayName = strings.TrimSpace(in.DisplayName)
	a.NativeAccountID = strings.TrimSpace(in.NativeAccountID)
	a.Regions = in.Regions
	a.CredentialsRef = strings.TrimSpace(in.CredentialsRef)
	a.Config = in.Config
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if in.DiscoveryIntervalSec > 0 {
		a.DiscoveryIntervalSec = in.DiscoveryIntervalSec
	}
	if in.MetricIntervalSec > 0 {
		a.MetricIntervalSec = in.MetricIntervalSec
	}
	return *a, nil
}

// DeleteAccount removes an account. Its resources are reported so the caller can
// warn about what will stop being monitored.
func (s *Store) DeleteAccount(id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i := range s.accounts {
		if s.accounts[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, store.ErrAccountNotFound
	}
	affected := s.accounts[idx].ResourceCount
	s.accounts = append(s.accounts[:idx], s.accounts[idx+1:]...)
	return affected, nil
}

// VerifyResult is the outcome of a connectivity check.
type VerifyResult struct {
	OK      bool    `json:"ok"`
	Checks  []Check `json:"checks"`
	Message string  `json:"message"`
}

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | failed | skipped
	Detail string `json:"detail,omitempty"`
}

// VerifyAccount checks whether an account is usable. The checks themselves are
// shared with the PostgreSQL backend so both report identically.
func (s *Store) VerifyAccount(id string, statCredential func(path string) (bool, bool, error)) (store.VerifyResult, error) {
	acct, ok := s.Account(id)
	if !ok {
		return store.VerifyResult{}, store.ErrAccountNotFound
	}
	return store.BuildVerifyResult(acct, statCredential), nil
}

// ServiceView lists every resource type the provider supports with how many of
// each this account currently has. Types with zero instances are included on
// purpose: knowing a service is integrated but empty is different from not
// knowing about it at all.
func (s *Store) ServiceView(accountID string) ([]store.ServiceTile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var acct *model.CloudAccount
	for i := range s.accounts {
		if s.accounts[i].ID == accountID {
			acct = &s.accounts[i]
			break
		}
	}
	if acct == nil {
		return nil, store.ErrAccountNotFound
	}

	counts := map[string]int{}
	unhealthy := map[string]int{}
	for _, r := range s.resources {
		if r.CloudAccountID != accountID || r.DeletedAt != nil {
			continue
		}
		counts[r.ResourceType]++
		if model.IsUnhealthy(r.Status) {
			unhealthy[r.ResourceType]++
		}
	}

	var out []store.ServiceTile
	for _, t := range catalog.ByProvider(acct.Provider) {
		out = append(out, store.ServiceTile{
			ResourceType: t.Code,
			DisplayName:  t.DisplayName,
			Icon:         t.Icon,
			Category:     t.Category,
			Count:        counts[t.Code],
			Unhealthy:    unhealthy[t.Code],
			Enabled:      true,
		})
	}
	return out, nil
}

// Account returns one account by id.
func (s *Store) Account(id string) (model.CloudAccount, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.accounts {
		if a.ID == id {
			return a, true
		}
	}
	return model.CloudAccount{}, false
}
