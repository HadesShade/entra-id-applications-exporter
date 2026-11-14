# entra-id-applications-exporter

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A Prometheus exporter for **Microsoft Entra ID app registrations**. It reads the
applications of one tenant through Microsoft Graph and exposes their secret and
certificate expiry dates, owner counts and basic metadata, so you can alert
**before** a credential expires and breaks something.

- Per-credential expiry for **secrets and certificates**, with their descriptions.
- Authenticates with a service principal (client secret).
- Supports the `public`, `china` and `usgov` clouds.
- Every scrape queries Graph live; if Graph throttles (HTTP 429) the last good
  result is served for a limited time.
- Single static binary, no dependencies. One exporter instance = one tenant / cloud.
- Ready-made [Grafana dashboard](#grafana-dashboard) and example alert rules.

## Installation

### Pre-built binary

Download the archive for your platform from the
[Releases](https://github.com/hadesshade/entra-id-applications-exporter/releases) page,
verify it against `checksums.txt`, and unpack it:

```sh
sha256sum -c checksums.txt --ignore-missing
tar -xzf entra-id-applications-exporter_<version>_linux_amd64.tar.gz
```

### Docker

```sh
git clone https://github.com/hadesshade/entra-id-applications-exporter.git
cd entra-id-applications-exporter
docker build -t entra-id-applications-exporter .

docker run -d --name entra-id-applications-exporter -p 9102:9102 \
  -e AZURE_TENANT_ID=<tenant-id> \
  -e AZURE_CLIENT_ID=<client-id> \
  -e AZURE_CLIENT_SECRET=<client-secret> \
  entra-id-applications-exporter
```

### From source

Requires the Go version listed in [`go.mod`](go.mod).

```sh
go install github.com/hadesshade/entra-id-applications-exporter/cmd/entra-id-applications-exporter@latest
```

or clone the repository and run `make build` (the binary lands in `bin/`).

## Quick start

1. [Create a service principal](#service-principal) and note its tenant ID, client ID and secret.
2. Copy the example config and fill it in:

   ```sh
   cp config.example.yaml config.yaml
   ```

3. Run the exporter and look at the metrics:

   ```sh
   ./entra-id-applications-exporter --config-file config.yaml
   curl localhost:9102/metrics | grep azure_entra_
   ```

## Service principal

Create the service principal in the **same cloud and tenant** you want to scrape.
In the Entra admin center / Azure portal:

1. **App registrations → New registration.** Name it, for example,
   `sp-entra-id-applications-exporter`. Single tenant is enough.
2. **Certificates & secrets → New client secret.** Copy the secret value now, it is
   shown only once.
3. **API permissions → Add a permission → Microsoft Graph → Application permissions →
   `Application.Read.All`.**
4. **Grant admin consent** for the tenant.

No Azure RBAC role is needed. The exporter's own client secret expires too, so
track it: this exporter can monitor itself if its app registration is in the tenant.

## Configuration

Precedence, lowest to highest: defaults < `$AZURE_*` environment variables <
CLI flags < config file. Every key present in the config file overrides the
matching flag. Unknown keys in the file are rejected.

| Config key / flag | Default | Description |
|---|---|---|
| `listen_address` / `--listen-address` | `:9102` | Address to expose metrics on. |
| `metrics_path` / `--metrics-path` | `/metrics` | HTTP path for metrics. Must start with `/`. |
| `log_level` / `--log-level` | `info` | `debug`, `info`, `warn` or `error`. |
| `scrape_timeout` / `--scrape-timeout` | `60s` | Time budget for one Graph scrape. |
| `cloud` / `--cloud` | `public` | `public`, `china` or `usgov`. |
| `tenant_id` / `--tenant-id` | `$AZURE_TENANT_ID` | Tenant to scrape. Required. |
| `client_id` / `--client-id` | `$AZURE_CLIENT_ID` | Service principal application (client) ID. Required. |
| `client_secret` / `--client-secret` | `$AZURE_CLIENT_SECRET` | Service principal secret. Required. Prefer the env var or the config file: CLI arguments are visible in `ps`. |
| `cache_max_age` / `--cache-max-age` | `10m` | How long cached data may be served while Graph answers 429. `0` disables the cache. |

The exporter also serves `GET /healthz`, which returns `ok`.

## Metrics

All metrics are gauges. Timestamps are Unix time in seconds.

| Metric | Labels | Description |
|---|---|---|
| `azure_entra_application_info` | `app_id`, `object_id`, `app_name`, `sign_in_audience` | Always `1`. Carries the application's metadata as labels. |
| `azure_entra_application_secret_expiration_timestamp_seconds` | `app_id`, `app_name`, `secret_id`, `secret_name` | Expiry time of one client secret. One series per secret. `secret_name` is the secret's description in Entra (may be empty). |
| `azure_entra_application_certificate_expiration_timestamp_seconds` | `app_id`, `app_name`, `certificate_id`, `certificate_name` | Expiry time of one certificate. One series per certificate. `certificate_name` is the certificate's description in Entra (may be empty). |
| `azure_entra_application_credentials_count` | `app_id`, `app_name`, `type` (`secret` or `certificate`) | Number of credentials of each type. Both types are always emitted, `0` included. |
| `azure_entra_application_owners_count` | `app_id`, `app_name` | Number of owners assigned to the application. |
| `azure_entra_application_created_timestamp_seconds` | `app_id` | Creation time of the application registration. |
| `azure_entra_applications_count` | none | Total number of application registrations in the tenant. |

Exporter health:

| Metric | Description |
|---|---|
| `azure_entra_up` | `1` if metrics were produced (live or cached), `0` if the scrape failed. |
| `azure_entra_scrape_duration_seconds` | Duration of the last Graph scrape. |
| `azure_entra_data_age_seconds` | `0` for live data, greater than `0` when served from cache because Graph throttled. Only emitted on a successful scrape. |

Notes:

- Credentials that are already expired are still reported, with an expiry in the
  past. Credentials without an end date are skipped.
- `app_name` is the application's display name and can change. `app_id` is the
  stable identifier.
- `secret_name` and `certificate_name` are the credential's description as shown in
  the Entra portal (Graph calls it `displayName`). They are optional, so the label can
  be empty. `secret_id` and `certificate_id` are the stable identifiers of a credential (the key ID in Graph, shown as "Secret ID" in the portal).

## Example queries and alerts

Days until each secret expires:

```promql
(azure_entra_application_secret_expiration_timestamp_seconds - time()) / 86400
```

Example alerting rules:

```yaml
groups:
  - name: entra-applications
    rules:
      - alert: EntraSecretExpiringSoon
        expr: azure_entra_application_secret_expiration_timestamp_seconds - time() < 30 * 24 * 3600
        for: 1h
        labels: { severity: warning }
        annotations:
          summary: "Secret '{{ $labels.secret_name }}' ({{ $labels.secret_id }}) of {{ $labels.app_name }} expires in under 30 days"

      - alert: EntraCertificateExpiringSoon
        expr: azure_entra_application_certificate_expiration_timestamp_seconds - time() < 30 * 24 * 3600
        for: 1h
        labels: { severity: warning }
        annotations:
          summary: "Certificate '{{ $labels.certificate_name }}' ({{ $labels.certificate_id }}) of {{ $labels.app_name }} expires in under 30 days"

      - alert: EntraCredentialExpired
        expr: |
          azure_entra_application_secret_expiration_timestamp_seconds < time()
          or
          azure_entra_application_certificate_expiration_timestamp_seconds < time()
        labels: { severity: critical }
        annotations:
          summary: "A credential of {{ $labels.app_name }} has expired"

      - alert: EntraApplicationWithoutOwner
        expr: azure_entra_application_owners_count == 0
        for: 1d
        labels: { severity: info }
        annotations:
          summary: "{{ $labels.app_name }} has no owner"

      - alert: EntraExporterDown
        expr: azure_entra_up == 0
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "The Entra applications exporter cannot read Microsoft Graph"
```

Expired credentials stay in the output, so the "expiring soon" rules also match
credentials that already expired. Silence old, unused credentials by deleting them
in Entra.

## Prometheus scrape config

```yaml
scrape_configs:
  - job_name: entra-applications
    scrape_interval: 10m
    scrape_timeout: 60s
    static_configs:
      - targets: ["localhost:9102"]
```

Credential expiry changes slowly, so scraping every 5 to 15 minutes is plenty.
Keep `scrape_timeout` in Prometheus at or above the exporter's own `scrape_timeout`.

## Throttling

Every scrape calls Graph live. On HTTP 429 the last good data is served for up to
`cache_max_age` and `azure_entra_data_age_seconds` is greater than `0`. Any other
error is never hidden by the cache: `azure_entra_up` becomes `0` and no
application metrics are emitted.

## Grafana dashboard

[`dashboards/entra-id-applications.json`](dashboards/entra-id-applications.json) is a
ready-made dashboard (Grafana 10 or newer). Import it with **Dashboards → New →
Import**, then pick your Prometheus data source. It shows:

- exporter status, expired and soon-to-expire credentials, and apps without owners
- a table of every secret and certificate sorted by days left
- the soonest expiry per application, and an application inventory
- exporter health: up, scrape duration and data age

## Running as a service (systemd)

```ini
# /etc/systemd/system/entra-id-applications-exporter.service
[Unit]
Description=Entra ID applications exporter
After=network-online.target
Wants=network-online.target

[Service]
User=entra-exporter
ExecStart=/usr/local/bin/entra-id-applications-exporter --config-file /etc/entra-id-applications-exporter/config.yaml
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

Keep the config file readable only by the service user (`chmod 600`).

## Security notes

- The service principal only needs the **read-only** `Application.Read.All`
  permission. Do not give it more.
- Treat `client_secret` like a password. Prefer the `AZURE_CLIENT_SECRET`
  environment variable or a config file with strict permissions; command-line
  arguments are visible in `ps`. Never commit `config.yaml` (it is in `.gitignore`).
- The metrics endpoint has no authentication or TLS and exposes application names and
  IDs. Bind it to a private network or put it behind a reverse proxy.

## Development

```sh
make test      # go test ./...
make lint      # go vet ./...
make build     # static binary in bin/
make docker    # build the container image
```

Where to change things:

- Query and data extraction: `internal/graph/applications.go`
- Metric names, labels and mapping: `internal/exporter/exporter.go`
- Config options: `internal/config/config.go`
- Exporter tests: `internal/exporter/exporter_test.go`

When adding a metric, declare its `*prometheus.Desc` in the struct, create it in
`New()`, list it in `Describe()` and emit it in `emit()`. The tests fail if a metric
is emitted but not described.

### Building a release

```sh
make release VERSION=v0.1.0
```

This writes `linux/amd64` and `linux/arm64` archives plus `checksums.txt` to `dist/`,
ready to attach to a GitHub release.

## Contributing

Issues and pull requests are welcome. Please run `make test lint` before opening a
pull request, and include a test for new metrics or behavior.

## License

[MIT](LICENSE) © 2026 HadesShade