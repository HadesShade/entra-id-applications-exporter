// Package config handles CLI flags and the optional YAML config file.
//
// Precedence (lowest -> highest):
//
//	defaults < environment (AZURE_*) < CLI flags < config file
//
// When --config-file is given, every key present in the file overrides the
// CLI value. Keys absent from the file keep their CLI value.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ConfigFile string `yaml:"-"` // CLI only

	ListenAddress string        `yaml:"listen_address"`
	MetricsPath   string        `yaml:"metrics_path"`
	LogLevel      string        `yaml:"log_level"`
	ScrapeTimeout time.Duration `yaml:"scrape_timeout"` // time budget for one Graph scrape

	// Service principal (must live in the tenant/cloud you want to scrape).
	Cloud        string `yaml:"cloud"` // public | china | usgov
	TenantID     string `yaml:"tenant_id"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`

	// CacheMaxAge: the last good result is served for up to this long, but ONLY
	// while Graph answers 429 (throttled). 0 disables the cache.
	CacheMaxAge time.Duration `yaml:"cache_max_age"`
}

// Load parses args (usually os.Args[1:]), then applies the config file if any.
func Load(args []string) (*Config, error) {
	cfg := &Config{}

	fs := flag.NewFlagSet("entra-app-exporter", flag.ContinueOnError)
	fs.StringVar(&cfg.ConfigFile, "config-file", "", "Path to YAML config file. Values in the file override CLI flags.")
	fs.StringVar(&cfg.ListenAddress, "listen-address", ":9102", "Address to expose metrics on.")
	fs.StringVar(&cfg.MetricsPath, "metrics-path", "/metrics", "HTTP path for metrics.")
	fs.StringVar(&cfg.LogLevel, "log-level", "info", "Log level: debug, info, warn, error.")
	fs.DurationVar(&cfg.ScrapeTimeout, "scrape-timeout", 60*time.Second, "Time budget for one scrape of Microsoft Graph.")

	fs.StringVar(&cfg.Cloud, "cloud", "public", "Azure cloud: public, china, usgov.")
	fs.StringVar(&cfg.TenantID, "tenant-id", os.Getenv("AZURE_TENANT_ID"), "Tenant ID (default: $AZURE_TENANT_ID).")
	fs.StringVar(&cfg.ClientID, "client-id", os.Getenv("AZURE_CLIENT_ID"), "Service principal client/app ID (default: $AZURE_CLIENT_ID).")
	fs.StringVar(&cfg.ClientSecret, "client-secret", os.Getenv("AZURE_CLIENT_SECRET"),
		"Service principal secret (default: $AZURE_CLIENT_SECRET). Prefer env var or config file: CLI args are visible in `ps`.")

	fs.DurationVar(&cfg.CacheMaxAge, "cache-max-age", 10*time.Minute,
		"Max age of cached data served while Graph is throttling (429). 0 disables the cache.")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if cfg.ConfigFile != "" {
		if err := cfg.applyFile(cfg.ConfigFile); err != nil {
			return nil, err
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyFile decodes onto the existing struct: yaml.v3 only overwrites fields
// whose keys are present in the file, giving "file overrides CLI" semantics.
func (c *Config) applyFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config file: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true) // typos in keys fail loudly
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}
	return nil
}

func (c *Config) validate() error {
	switch strings.ToLower(c.Cloud) {
	case "public", "china", "usgov":
	default:
		return fmt.Errorf("cloud %q is invalid (use public, china or usgov)", c.Cloud)
	}
	switch {
	case c.TenantID == "":
		return errors.New("tenant_id is required (--tenant-id, $AZURE_TENANT_ID or config file)")
	case c.ClientID == "":
		return errors.New("client_id is required (--client-id, $AZURE_CLIENT_ID or config file)")
	case c.ClientSecret == "":
		return errors.New("client_secret is required (prefer $AZURE_CLIENT_SECRET or config file)")
	case !strings.HasPrefix(c.MetricsPath, "/"):
		return fmt.Errorf("metrics_path %q must start with '/'", c.MetricsPath)
	case c.ScrapeTimeout <= 0:
		return errors.New("scrape_timeout must be > 0")
	case c.CacheMaxAge < 0:
		return errors.New("cache_max_age must be >= 0")
	}
	return nil
}
