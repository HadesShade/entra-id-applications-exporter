// Package exporter turns Graph data into Prometheus metrics.
//
// Every Prometheus scrape triggers a LIVE Graph call. The last good result is
// kept in memory and served ONLY when Graph answers 429 (throttled). Any other
// error is never hidden by the cache: azure_entra_up becomes 0.
package exporter

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hadesshade/entra-id-applications-exporter/internal/graph"
	"github.com/prometheus/client_golang/prometheus"
)

// AppLister is what the exporter needs from the Graph client (also eases testing).
type AppLister interface {
	ListApplications(ctx context.Context) ([]graph.Application, error)
}

type Options struct {
	ScrapeTimeout time.Duration
	CacheMaxAge   time.Duration // 0 disables the cache

	// LatestCredentialsOnly limits the expiry metrics to the latest secret and
	// the latest certificate of each application.
	LatestCredentialsOnly bool
}

type Exporter struct {
	lister AppLister
	opts   Options
	logger *slog.Logger

	mu       sync.Mutex // serialises scrapes: no duplicate Graph calls while throttled
	cache    []graph.Application
	cachedAt time.Time
	hasCache bool

	// exporter health metrics
	up             *prometheus.Desc
	scrapeDuration *prometheus.Desc
	dataAge        *prometheus.Desc

	// Entra ID applications metrics
	applicationInfo   *prometheus.Desc
	secretExpiry      *prometheus.Desc
	certificateExpiry *prometheus.Desc
	credentialsCount  *prometheus.Desc
	ownersCount       *prometheus.Desc
	createdTimestamp  *prometheus.Desc
	applicationsCount *prometheus.Desc
}

func New(l AppLister, opts Options, logger *slog.Logger) *Exporter {
	d := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName("azure", "entra", name), help, labels, nil)
	}
	return &Exporter{
		lister: l, opts: opts, logger: logger,

		up:             d("up", "1 if metrics were produced (live or cached data), 0 if the scrape failed."),
		scrapeDuration: d("scrape_duration_seconds", "Duration of the last Graph scrape."),
		dataAge:        d("data_age_seconds", "Age of the data in this scrape: 0 when live, >0 when served from cache because Graph throttled us."),

		applicationInfo:   d("application_info", "Application registration info (always 1).", "app_id", "object_id", "app_name", "sign_in_audience"),
		secretExpiry:      d("application_secret_expiration_timestamp_seconds", "Expiration time of an application secret, as a Unix timestamp in seconds.", "app_id", "app_name", "secret_id", "secret_name"),
		certificateExpiry: d("application_certificate_expiration_timestamp_seconds", "Expiration time of an application certificate, as a Unix timestamp in seconds.", "app_id", "app_name", "certificate_id", "certificate_name"),
		credentialsCount:  d("application_credentials_count", "Number of credentials on the application registration, by type (secret or certificate).", "app_id", "app_name", "type"),
		ownersCount:       d("application_owners_count", "Number of owners assigned to the application registration.", "app_id", "app_name"),
		createdTimestamp:  d("application_created_timestamp_seconds", "Creation time of the application registration, as a Unix timestamp in seconds.", "app_id"),
		applicationsCount: d("applications_count", "Total number of application registrations found in the tenant."),
	}
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		e.up,
		e.scrapeDuration,
		e.dataAge,
		e.applicationInfo,
		e.secretExpiry,
		e.certificateExpiry,
		e.credentialsCount,
		e.ownersCount,
		e.createdTimestamp,
		e.applicationsCount,
	} {
		ch <- d
	}
}

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mu.Lock()
	defer e.mu.Unlock()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), e.opts.ScrapeTimeout)
	defer cancel()

	apps, age, err := e.load(ctx)

	up := 0.0
	if err != nil {
		e.logger.Error("graph scrape failed", "err", err)
	} else {
		up = 1.0
		e.emit(ch, apps)
		ch <- prometheus.MustNewConstMetric(e.dataAge, prometheus.GaugeValue, age.Seconds())
	}
	ch <- prometheus.MustNewConstMetric(e.up, prometheus.GaugeValue, up)
	ch <- prometheus.MustNewConstMetric(e.scrapeDuration, prometheus.GaugeValue, time.Since(start).Seconds())
}

// load returns live data, or cached data (age > 0) only when Graph throttles us.
func (e *Exporter) load(ctx context.Context) ([]graph.Application, time.Duration, error) {
	apps, err := e.lister.ListApplications(ctx)
	if err == nil {
		e.cache, e.cachedAt, e.hasCache = apps, time.Now(), true
		return apps, 0, nil
	}
	if graph.IsThrottled(err) && e.opts.CacheMaxAge > 0 && e.hasCache {
		if age := time.Since(e.cachedAt); age <= e.opts.CacheMaxAge {
			e.logger.Warn("Graph throttled; serving cached data", "age", age.Round(time.Second))
			return e.cache, age, nil
		}
	}
	return nil, 0, err
}

// emit maps data to metrics. Called only after a successful fetch (live or cached).
func (e *Exporter) emit(ch chan<- prometheus.Metric, apps []graph.Application) {
	applicationsCount := 0
	for _, app := range apps {
		secrets, certificates := app.Secrets, app.Certificates
		if e.opts.LatestCredentialsOnly {
			secrets, certificates = latestOnly(secrets), latestOnly(certificates)
		}

		for _, secret := range secrets {
			ch <- prometheus.MustNewConstMetric(
				e.secretExpiry, prometheus.GaugeValue,
				float64(secret.ExpiryTime.Unix()),
				app.AppID, app.Name, secret.KeyID, secret.Name,
			)
		}

		for _, certificate := range certificates {
			ch <- prometheus.MustNewConstMetric(
				e.certificateExpiry, prometheus.GaugeValue,
				float64(certificate.ExpiryTime.Unix()),
				app.AppID, app.Name, certificate.KeyID, certificate.Name,
			)
		}

		ch <- prometheus.MustNewConstMetric(
			e.applicationInfo, prometheus.GaugeValue,
			1, app.AppID, app.ObjectID, app.Name, app.Audience,
		)

		ch <- prometheus.MustNewConstMetric(
			e.credentialsCount, prometheus.GaugeValue,
			float64(len(secrets)), app.AppID, app.Name, "secret",
		)

		ch <- prometheus.MustNewConstMetric(
			e.credentialsCount, prometheus.GaugeValue,
			float64(len(certificates)), app.AppID, app.Name, "certificate",
		)

		ch <- prometheus.MustNewConstMetric(
			e.ownersCount, prometheus.GaugeValue,
			float64(app.OwnersCount), app.AppID, app.Name,
		)

		ch <- prometheus.MustNewConstMetric(
			e.createdTimestamp, prometheus.GaugeValue,
			float64(app.Created.Unix()), app.AppID,
		)

		applicationsCount++
	}

	ch <- prometheus.MustNewConstMetric(
		e.applicationsCount, prometheus.GaugeValue,
		float64(applicationsCount),
	)
}

// latestOnly returns the credentials that expire last. Credentials sharing the
// exact same expiry are all kept: nothing is hidden when "latest" is ambiguous.
// If every credential is expired, the latest expired one is still returned, so
// an application with only dead credentials keeps alerting.
// credentials_count is not affected: it keeps reporting what exists in Entra.
func latestOnly(creds []graph.Credential) []graph.Credential {
	if len(creds) <= 1 {
		return creds
	}
	latest := creds[0].ExpiryTime
	for _, c := range creds[1:] {
		if c.ExpiryTime.After(latest) {
			latest = c.ExpiryTime
		}
	}
	out := make([]graph.Credential, 0, 1)
	for _, c := range creds {
		if c.ExpiryTime.Equal(latest) {
			out = append(out, c)
		}
	}
	return out
}
