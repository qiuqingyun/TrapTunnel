package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadNodeEdgeDefaults(t *testing.T) {
	cfg := loadNodeForTest(t, `
[node]
id = 101
name = "edge-a"
profile = "edge"

[[egress.groups]]
members = ["172.16.1.10:10000", "172.16.1.11:10000"]
`)

	if cfg.Profile != ProfileEdge {
		t.Fatalf("unexpected profile: %q", cfg.Profile)
	}
	if !cfg.Capture.Enabled || !cfg.Egress.Enabled {
		t.Fatalf("edge profile did not enable capture+egress: %+v", cfg)
	}
	if len(cfg.Capture.ListenPorts) != 1 || cfg.Capture.ListenPorts[0] != 162 {
		t.Fatalf("unexpected capture defaults: %+v", cfg.Capture)
	}
	if cfg.Egress.ReconnectInterval != 5 {
		t.Fatalf("unexpected egress reconnect default: %+v", cfg.Egress)
	}
	if cfg.Logging.Level != "INFO" || cfg.Logging.MaxLogSize != 10 || cfg.Logging.MaxLogBackups != 100 {
		t.Fatalf("unexpected logging defaults: %+v", cfg.Logging)
	}
}

func TestLoadNodeSinkDefaults(t *testing.T) {
	cfg := loadNodeForTest(t, `
[node]
profile = "sink"
`)

	if !cfg.Ingress.Enabled || !cfg.Inject.Enabled {
		t.Fatalf("sink profile did not enable ingress+inject: %+v", cfg)
	}
	if cfg.Ingress.Listen != "0.0.0.0:10000" {
		t.Fatalf("unexpected ingress default: %+v", cfg.Ingress)
	}
	if cfg.Inject.IP != "127.0.0.1" || cfg.Inject.Port != 162 {
		t.Fatalf("unexpected inject defaults: %+v", cfg.Inject)
	}
}

func TestLoadNodeRejectsUnsupportedCapability(t *testing.T) {
	_, err := loadNodeErrForTest(t, `
[node]
id = 101
profile = "edge"

[[egress.groups]]
members = ["172.16.1.10:10000"]

[inject]
enabled = true
ip = "127.0.0.1"
port = 1162
`)
	if err == nil || !strings.Contains(err.Error(), `does not support inject.enabled`) {
		t.Fatalf("expected unsupported capability error, got %v", err)
	}
}

func TestLoadNodeRejectsUnknownFields(t *testing.T) {
	_, err := loadNodeErrForTest(t, `
[node]
profile = "sink"
unexpected = "value"
`)
	if err == nil || !strings.Contains(err.Error(), "unknown fields") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func loadNodeForTest(t *testing.T, content string) NodeConfig {
	t.Helper()

	cfg, err := loadNodeErrForTest(t, content)
	if err != nil {
		t.Fatalf("LoadNode() error = %v", err)
	}
	return cfg
}

func loadNodeErrForTest(t *testing.T, content string) (NodeConfig, error) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "node.toml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return LoadNode(path)
}
