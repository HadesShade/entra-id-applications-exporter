package exporter

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hadesshade/entra-id-applications-exporter/internal/graph"
	"github.com/prometheus/client_golang/prometheus"
)

// Stubs and helpers
type stub struct{ results []error } // nil = success, else the error to return

func (s *stub) ListApplications(context.Context) ([]graph.Application, error) {
	err := s.results[0]
	s.results = s.results[1:]
	if err != nil {
		return nil, err
	}
	return []graph.Application{{}}, nil
}

// fixedLister always succeeds and returns the given applications.
type fixedLister struct{ apps []graph.Application }

func (f fixedLister) ListApplications(context.Context) ([]graph.Application, error) {
	return f.apps, nil
}

// sample is one exported series: its labels and its value.
type sample struct {
	labels map[string]string
	value  float64
}

func newTestExporter(l AppLister) *Exporter {
	return New(l, Options{ScrapeTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// gather registers the exporter in a fresh registry, scrapes it once and
// returns every series grouped by metric name. Gather also fails if a metric
// is emitted without being declared in Describe(), so that mistake is caught here.
func gather(t *testing.T, e *Exporter) map[string][]sample {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(e)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := make(map[string][]sample)
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			s := sample{labels: map[string]string{}, value: m.GetGauge().GetValue()}
			for _, lp := range m.GetLabel() {
				s.labels[lp.GetName()] = lp.GetValue()
			}
			out[mf.GetName()] = append(out[mf.GetName()], s)
		}
	}
	return out
}

// valueOf returns the value of the first sample whose labels include all of want.
func valueOf(t *testing.T, samples []sample, want map[string]string) float64 {
	t.Helper()
	for _, s := range samples {
		match := true
		for k, v := range want {
			if s.labels[k] != v {
				match = false
				break
			}
		}
		if match {
			return s.value
		}
	}
	t.Fatalf("no sample with labels %v; series present: %+v", want, samples)
	return 0
}

func expect(t *testing.T, what string, got, want float64) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func up(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "azure_entra_up" {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatal("azure_entra_up not found")
	return 0
}

// Metric names under test.
const (
	mInfo        = "azure_entra_application_info"
	mSecretExp   = "azure_entra_application_secret_expiration_timestamp_seconds"
	mCertExp     = "azure_entra_application_certificate_expiration_timestamp_seconds"
	mCredsCount  = "azure_entra_application_credentials_count"
	mOwnersCount = "azure_entra_application_owners_count"
	mCreated     = "azure_entra_application_created_timestamp_seconds"
	mAppsCount   = "azure_entra_applications_count"
)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCacheOnlyServedWhenThrottled(t *testing.T) {
	l := &stub{results: []error{nil, &graph.ThrottledError{}, errors.New("boom")}}
	e := New(l, Options{ScrapeTimeout: time.Second, CacheMaxAge: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg := prometheus.NewRegistry()
	reg.MustRegister(e)

	if v := up(t, reg); v != 1 { // live
		t.Fatalf("scrape 1: up=%v", v)
	}
	if v := up(t, reg); v != 1 { // throttled -> cached
		t.Fatalf("scrape 2: up=%v", v)
	}
	if v := up(t, reg); v != 0 { // other error -> not masked
		t.Fatalf("scrape 3: up=%v", v)
	}
}

// An app with two secrets and one certificate: secrets and certificates must
// end up in their own expiration metrics and be counted per type.
func TestSecretsAndCertificatesAreSeparateMetrics(t *testing.T) {
	created := time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC)
	secret1 := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	secret2 := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	cert1 := time.Date(2028, 3, 20, 0, 0, 0, 0, time.UTC)

	apps := []graph.Application{{
		ObjectID:    "obj-1",
		AppID:       "app-1",
		Name:        "my-app",
		Audience:    "AzureADMyOrg",
		Created:     created,
		OwnersCount: 2,
		Secrets: []graph.Credential{
			{Name: "first secret", KeyID: "secret-1", ExpiryTime: secret1},
			{Name: "second secret", KeyID: "secret-2", ExpiryTime: secret2},
		},
		Certificates: []graph.Credential{
			{Name: "my certificate", KeyID: "cert-1", ExpiryTime: cert1},
		},
	}}

	m := gather(t, newTestExporter(fixedLister{apps}))

	// Expiration series: one per credential, in the right metric.
	secrets, certs := m[mSecretExp], m[mCertExp]
	if len(secrets) != 2 {
		t.Fatalf("secret expiration series = %d, want 2", len(secrets))
	}
	if len(certs) != 1 {
		t.Fatalf("certificate expiration series = %d, want 1", len(certs))
	}
	for _, s := range secrets {
		if s.labels["secret_id"] == "cert-1" {
			t.Error("certificate leaked into the secret expiration metric")
		}
	}
	for _, s := range certs {
		if s.labels["certificate_id"] == "secret-1" || s.labels["certificate_id"] == "secret-2" {
			t.Error("secret leaked into the certificate expiration metric")
		}
	}
	expect(t, "secret-1 expiry", valueOf(t, secrets, map[string]string{"app_id": "app-1", "app_name": "my-app", "secret_id": "secret-1", "secret_name": "first secret"}), float64(secret1.Unix()))
	expect(t, "secret-2 expiry", valueOf(t, secrets, map[string]string{"secret_id": "secret-2", "secret_name": "second secret"}), float64(secret2.Unix()))
	expect(t, "cert-1 expiry", valueOf(t, certs, map[string]string{"app_id": "app-1", "app_name": "my-app", "certificate_id": "cert-1", "certificate_name": "my certificate"}), float64(cert1.Unix()))

	// Credential counts: one series per type.
	counts := m[mCredsCount]
	if len(counts) != 2 {
		t.Fatalf("credentials_count series = %d, want 2", len(counts))
	}
	expect(t, "secret count", valueOf(t, counts, map[string]string{"app_id": "app-1", "app_name": "my-app", "type": "secret"}), 2)
	expect(t, "certificate count", valueOf(t, counts, map[string]string{"app_id": "app-1", "app_name": "my-app", "type": "certificate"}), 1)

	// The remaining per-app metrics.
	expect(t, "info", valueOf(t, m[mInfo], map[string]string{
		"app_id": "app-1", "object_id": "obj-1", "app_name": "my-app", "sign_in_audience": "AzureADMyOrg",
	}), 1)
	expect(t, "owners count", valueOf(t, m[mOwnersCount], map[string]string{"app_id": "app-1", "app_name": "my-app"}), 2)
	expect(t, "created", valueOf(t, m[mCreated], map[string]string{"app_id": "app-1"}), float64(created.Unix()))
	expect(t, "applications count", valueOf(t, m[mAppsCount], nil), 1)
}

// An app with no credentials still reports both counts as 0 and no expiration series.
func TestAppWithoutCredentials(t *testing.T) {
	apps := []graph.Application{{AppID: "app-2", Name: "bare-app"}}

	m := gather(t, newTestExporter(fixedLister{apps}))

	if n := len(m[mSecretExp]); n != 0 {
		t.Errorf("secret expiration series = %d, want 0", n)
	}
	if n := len(m[mCertExp]); n != 0 {
		t.Errorf("certificate expiration series = %d, want 0", n)
	}
	counts := m[mCredsCount]
	if len(counts) != 2 {
		t.Fatalf("credentials_count series = %d, want 2", len(counts))
	}
	expect(t, "secret count", valueOf(t, counts, map[string]string{"type": "secret"}), 0)
	expect(t, "certificate count", valueOf(t, counts, map[string]string{"type": "certificate"}), 0)
	expect(t, "owners count", valueOf(t, m[mOwnersCount], map[string]string{"app_id": "app-2"}), 0)
}

// A tenant with no app registrations: only the total is reported, as 0.
func TestNoApplications(t *testing.T) {
	m := gather(t, newTestExporter(fixedLister{}))

	expect(t, "applications count", valueOf(t, m[mAppsCount], nil), 0)
	for _, name := range []string{mInfo, mSecretExp, mCertExp, mCredsCount, mOwnersCount, mCreated} {
		if n := len(m[name]); n != 0 {
			t.Errorf("%s has %d series, want 0", name, n)
		}
	}
}

// With LatestCredentialsOnly, each app keeps only its latest secret and latest
// certificate; credentials_count still reports everything.
func TestLatestCredentialsOnly(t *testing.T) {
	d := func(y int, m time.Month) time.Time { return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC) }
	apps := []graph.Application{
		{
			AppID: "app-1", Name: "rotated-app",
			Secrets: []graph.Credential{
				{KeyID: "old", ExpiryTime: d(2024, 1)},
				{KeyID: "new", ExpiryTime: d(2027, 1)},
				{KeyID: "mid", ExpiryTime: d(2025, 6)},
			},
			Certificates: []graph.Credential{
				{KeyID: "cert-old", ExpiryTime: d(2024, 3)},
				{KeyID: "cert-new", ExpiryTime: d(2028, 3)},
			},
		},
		{ // every credential expired: the latest one must still be reported
			AppID: "app-2", Name: "dead-app",
			Secrets: []graph.Credential{
				{KeyID: "d1", ExpiryTime: d(2023, 1)},
				{KeyID: "d2", ExpiryTime: d(2024, 1)},
			},
		},
		{ // identical expiry: nothing is hidden
			AppID: "app-3", Name: "tie-app",
			Secrets: []graph.Credential{
				{KeyID: "t1", ExpiryTime: d(2027, 5)},
				{KeyID: "t2", ExpiryTime: d(2027, 5)},
			},
		},
	}
	e := New(fixedLister{apps}, Options{ScrapeTimeout: time.Second, LatestCredentialsOnly: true},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	m := gather(t, e)

	ids := map[string]bool{}
	for _, s := range m[mSecretExp] {
		ids[s.labels["secret_id"]] = true
	}
	for _, want := range []string{"new", "d2", "t1", "t2"} {
		if !ids[want] {
			t.Errorf("secret %q should be exported, got %v", want, ids)
		}
	}
	for _, gone := range []string{"old", "mid", "d1"} {
		if ids[gone] {
			t.Errorf("secret %q is superseded and must not be exported", gone)
		}
	}
	if n := len(m[mCertExp]); n != 1 || m[mCertExp][0].labels["certificate_id"] != "cert-new" {
		t.Errorf("certificate series = %+v, want only cert-new", m[mCertExp])
	}
	expect(t, "secret count is unfiltered", valueOf(t, m[mCredsCount], map[string]string{"app_id": "app-1", "type": "secret"}), 3)
}

// Default behaviour (option off) is unchanged: every credential is exported.
func TestAllCredentialsByDefault(t *testing.T) {
	apps := []graph.Application{{
		AppID: "a", Name: "n",
		Secrets: []graph.Credential{
			{KeyID: "s1", ExpiryTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
			{KeyID: "s2", ExpiryTime: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
	}}
	m := gather(t, newTestExporter(fixedLister{apps}))
	if n := len(m[mSecretExp]); n != 2 {
		t.Errorf("secret series = %d, want 2", n)
	}
}
