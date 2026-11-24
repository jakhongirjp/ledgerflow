package config

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndValidation(t *testing.T) {
	t.Setenv("LEDGER_STORE", "memory")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.RelayInterval != time.Second || c.RelayBatchSize != 100 {
		t.Fatalf("unexpected defaults %+v", c)
	}

	t.Setenv("LEDGER_STORE", "postgres")
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error without DATABASE_URL")
	}

	t.Setenv("LEDGER_STORE", "mongo")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for unknown store")
	}

	t.Setenv("LEDGER_STORE", "memory")
	t.Setenv("RELAY_BATCH_SIZE", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for negative batch size")
	}
}
