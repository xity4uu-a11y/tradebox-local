package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDirValidAndInvalidPlugins(t *testing.T) {
	dir := t.TempDir()

	validJSON := `{
  "id": "sample-latency-guard",
  "name": "Sample Latency Guard",
  "version": "1.0.0",
  "category": "latency",
  "author": "tradebox-community",
  "description": "Monitors broker execution RTT spikes and packet loss thresholds.",
  "min_tradebox_version": "1.0.0",
  "enabled_by_default": true,
  "tags": ["latency", "tcp"],
  "triggers": ["latency.probe"],
  "config": {"max_latency_ms": 120},
  "rules": [
    {"id": "high-rtt", "match": "rtt_ms > 120", "severity": "warn", "action": "dispatch_alert"}
  ]
}`
	invalidJSON := `{
  "id": "INVALID_ID",
  "name": "",
  "version": "bad",
  "category": "unknown",
  "author": "",
  "description": "short",
  "min_tradebox_version": "1.0",
  "tags": [],
  "triggers": [],
  "rules": []
}`
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01-valid.json"), []byte(validJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "02-invalid.json"), []byte(invalidJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir failed: %v", err)
	}
	if rep.Total != 2 || rep.Passed != 1 || rep.Failed != 1 {
		t.Fatalf("expected total=2 passed=1 failed=1, got %+v", rep)
	}
	if len(rep.Plugins) != 1 || rep.Plugins[0].ID != "sample-latency-guard" {
		t.Errorf("unexpected loaded plugins: %+v", rep.Plugins)
	}
}
