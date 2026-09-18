package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"nimbuseye/internal/store"
)

// registerAccountRoutes wires the Admin > Cloud Accounts endpoints.
func (s *Server) registerAccountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/cloud-accounts/providers", s.providerSpecs)
	mux.HandleFunc("GET /api/v1/cloud-accounts/{id}", s.getAccount)
	mux.HandleFunc("GET /api/v1/cloud-accounts/{id}/services", s.accountServices)
	mux.HandleFunc("POST /api/v1/cloud-accounts", s.createAccount)
	mux.HandleFunc("PATCH /api/v1/cloud-accounts/{id}", s.updateAccount)
	mux.HandleFunc("DELETE /api/v1/cloud-accounts/{id}", s.deleteAccount)
	mux.HandleFunc("POST /api/v1/cloud-accounts/{id}/verify", s.verifyAccount)
}

// providerSpecs tells the UI which fields each provider needs, so the form is
// generated from one definition instead of being duplicated in the frontend.
func (s *Server) providerSpecs(w http.ResponseWriter, r *http.Request) {
	type providerSpec struct {
		Provider             string            `json:"provider"`
		Label                string            `json:"label"`
		AccountIDLabel       string            `json:"account_id_label"`
		AccountIDPlaceholder string            `json:"account_id_placeholder"`
		Regions              []string          `json:"regions"`
		Fields               []store.FieldSpec `json:"fields"`
		Credential           store.FieldSpec   `json:"credential"`
		// Least-privilege guidance, shown in the form. Getting this wrong is the
		// most common way a monitoring integration ends up over-permissioned.
		Permissions []string `json:"permissions"`
	}

	regions := s.store.Regions()
	meta := []struct {
		key, label, idLabel, idPlaceholder string
		perms                              []string
	}{
		{"oci", "Oracle Cloud Infrastructure", "Tenancy OCID", "ocid1.tenancy.oc1..aaaa…",
			[]string{
				"Create a group and add a read-only user to it",
				"Policy: Allow group NimbusEyeReaders to read all-resources in tenancy",
				"Policy: Allow group NimbusEyeReaders to read metrics in tenancy",
				"Do not grant manage or use verbs — discovery and metrics need read only",
			}},
		{"aws", "Amazon Web Services", "Account ID", "123456789012",
			[]string{
				"Create a role NimbusEyeReadOnly trusted by the NimbusEye server",
				"Attach the AWS managed ReadOnlyAccess policy",
				"Add cloudwatch:GetMetricData and cloudwatch:ListMetrics",
				"Set an External ID on the trust policy",
			}},
		{"azure", "Microsoft Azure", "Subscription ID", "00000000-0000-0000-0000-000000000000",
			[]string{
				"Register an application and create a client secret",
				"Assign the built-in Reader role at subscription scope",
				"Assign Monitoring Reader for metric access",
			}},
		{"gcp", "Google Cloud Platform", "Project ID", "my-project-1234",
			[]string{
				"Create a service account with no keys beyond the one you download",
				"Grant roles/viewer and roles/monitoring.viewer",
				"Enable the Cloud Asset and Monitoring APIs on the project",
			}},
	}

	out := make([]providerSpec, 0, len(meta))
	for _, m := range meta {
		out = append(out, providerSpec{
			Provider: m.key, Label: m.label,
			AccountIDLabel: m.idLabel, AccountIDPlaceholder: m.idPlaceholder,
			Regions:     regions[m.key],
			Fields:      store.ProviderFields(m.key),
			Credential:  store.CredentialFieldFor(m.key),
			Permissions: m.perms,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.store.Account(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "account_not_found", "No cloud account with that id")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) accountServices(w http.ResponseWriter, r *http.Request) {
	tiles, err := s.store.ServiceView(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "account_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tiles, "total": len(tiles)})
}

func decodeAccountInput(r *http.Request) (store.AccountInput, error) {
	var in store.AccountInput
	// A modest cap: this payload is a handful of identifiers, and an unbounded
	// read on a public endpoint is an easy denial of service.
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		return in, err
	}
	return in, nil
}

func (s *Server) writeValidation(w http.ResponseWriter, err error) bool {
	var ve *store.ValidationError
	if errors.As(err, &ve) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": map[string]any{
				"code":    "validation_failed",
				"message": "Some fields need attention",
				"fields":  ve.Fields,
			},
		})
		return true
	}
	return false
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	in, err := decodeAccountInput(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	acct, err := s.store.AddAccount(in)
	if err != nil {
		if s.writeValidation(w, err) {
			return
		}
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	s.log.Info("cloud account added",
		"provider", acct.Provider, "account", acct.NativeAccountID, "id", acct.ID)
	writeJSON(w, http.StatusCreated, acct)
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	in, err := decodeAccountInput(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	acct, err := s.store.UpdateAccount(r.PathValue("id"), in)
	if err != nil {
		if s.writeValidation(w, err) {
			return
		}
		if errors.Is(err, store.ErrAccountNotFound) {
			writeError(w, http.StatusNotFound, "account_not_found", err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, acct)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	affected, err := s.store.DeleteAccount(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrAccountNotFound) {
			writeError(w, http.StatusNotFound, "account_not_found", err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, "delete_failed", err.Error())
		return
	}
	s.log.Warn("cloud account removed", "id", r.PathValue("id"), "resources_affected", affected)
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":            true,
		"resources_affected": affected,
	})
}

// statCredential reports whether a credential file exists and whether its
// permissions are too permissive.
//
// Reading the file is deliberately not done: the API never needs the secret, only
// the collector does, so this check is a stat and nothing more.
func statCredential(path string) (exists bool, tooOpen bool, err error) {
	if !strings.HasPrefix(path, "/") {
		return false, false, errors.New("path must be absolute")
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return false, false, nil
		}
		if errors.Is(statErr, fs.ErrPermission) {
			return false, false, errors.New("permission denied reading " + path)
		}
		return false, false, statErr
	}
	if fi.IsDir() {
		return false, false, errors.New("path is a directory")
	}
	// World access is a finding. Group access is not: the deployment pattern is a
	// root-owned file readable by the dedicated service group. This must agree
	// with what the collector enforces, or the UI would report a problem the
	// collector is happy with.
	return true, fi.Mode().Perm()&0o007 != 0, nil
}

func (s *Server) verifyAccount(w http.ResponseWriter, r *http.Request) {
	res, err := s.store.VerifyAccount(r.PathValue("id"), statCredential)
	if err != nil {
		writeError(w, http.StatusNotFound, "account_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
