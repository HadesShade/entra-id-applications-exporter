package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileOverridesCLI(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	yml := "tenant_id: file-tenant\ncloud: china\ncache_max_age: 2m\n"
	if err := os.WriteFile(p, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load([]string{
		"--config-file", p,
		"--tenant-id", "cli-tenant",
		"--client-id", "cid",
		"--client-secret", "secret",
		"--cloud", "public",
		"--listen-address", ":9999",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TenantID != "file-tenant" || cfg.Cloud != "china" || cfg.CacheMaxAge != 2*time.Minute {
		t.Errorf("file must override CLI, got tenant=%q cloud=%q cache=%v", cfg.TenantID, cfg.Cloud, cfg.CacheMaxAge)
	}
	if cfg.ClientID != "cid" || cfg.ListenAddress != ":9999" {
		t.Errorf("CLI values absent from the file must be kept")
	}
}

func TestInvalidCloud(t *testing.T) {
	_, err := Load([]string{"--tenant-id", "t", "--client-id", "c", "--client-secret", "s", "--cloud", "mars"})
	if err == nil {
		t.Fatal("expected error for invalid cloud")
	}
}

func TestLatestCredentialsOnly(t *testing.T) {
	base := []string{"--tenant-id", "t", "--client-id", "c", "--client-secret", "s"}

	cfg, err := Load(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LatestCredentialsOnly {
		t.Error("latest_credentials_only must default to false")
	}

	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("latest_credentials_only: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(append(base, "--config-file", p))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LatestCredentialsOnly {
		t.Error("latest_credentials_only: true in the file must enable the option")
	}
}
