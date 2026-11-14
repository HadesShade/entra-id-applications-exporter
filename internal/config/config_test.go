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
