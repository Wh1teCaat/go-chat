package config

import "testing"

func TestLoadDefaultsKafkaEnabled(t *testing.T) {
	// Load searches relative config paths. Run from a temporary directory so a
	// developer's ignored configs/config.toml cannot override the defaults.
	t.Chdir(t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.Kafka.Enabled {
		t.Fatal("Kafka must be enabled by default")
	}
}
