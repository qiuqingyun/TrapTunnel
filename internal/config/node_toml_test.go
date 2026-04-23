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
	if cfg.Tuning.PipelineBufferSize != 1024 ||
		cfg.Tuning.CaptureReadBufferSize != 2048 ||
		cfg.Tuning.CaptureReadBackoffMS != 100 ||
		cfg.Tuning.IngressReadBufferSize != 8192 ||
		cfg.Tuning.MaxFrameTotalLength != 10*1024*1024 ||
		cfg.Tuning.EgressGroupBufferSize != 1024 ||
		cfg.Tuning.EgressDialTimeoutMS != 5000 ||
		cfg.Tuning.EgressWriteTimeoutMS != 5000 ||
		cfg.Tuning.EgressBackoffMaxMS != 30000 ||
		cfg.Tuning.EgressBackoffJitterPct != 20 ||
		cfg.Tuning.ExportClientBufferSize != 1024 ||
		cfg.Tuning.ExportWriteTimeoutMS != 5000 {
		t.Fatalf("unexpected tuning defaults: %+v", cfg.Tuning)
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

func TestLoadNodeRelayAllowsOptionalExport(t *testing.T) {
	cfg := loadNodeForTest(t, `
[node]
id = 101
profile = "relay"

[ingress]
listen = "0.0.0.0:11000"

[[egress.groups]]
members = ["172.16.1.10:10000"]
`)

	if cfg.Export.Enabled {
		t.Fatalf("expected export to remain disabled by default: %+v", cfg.Export)
	}
}

func TestLoadNodeRelayAllowsEnabledExport(t *testing.T) {
	cfg := loadNodeForTest(t, `
[node]
id = 101
profile = "relay"

[ingress]
listen = "0.0.0.0:11000"

[[egress.groups]]
members = ["172.16.1.10:10000"]

[export]
enabled = true
listen = "0.0.0.0:12000"
format = "frame"
max_clients = 8
`)

	if !cfg.Export.Enabled || cfg.Export.Listen != "0.0.0.0:12000" {
		t.Fatalf("expected relay export to be enabled: %+v", cfg.Export)
	}
}

func TestLoadNodeExportDefaultsSlowClientPolicy(t *testing.T) {
	cfg := loadNodeForTest(t, `
[node]
profile = "sink"

[export]
enabled = true
listen = "0.0.0.0:12000"
`)

	if cfg.Export.SlowClientPolicy != "disconnect" {
		t.Fatalf("unexpected slow client policy default: %+v", cfg.Export)
	}
}

func TestLoadNodeRejectsInvalidExportSlowClientPolicy(t *testing.T) {
	_, err := loadNodeErrForTest(t, `
[node]
profile = "sink"

[export]
enabled = true
listen = "0.0.0.0:12000"
slow_client_policy = "unknown"
`)
	if err == nil || !strings.Contains(err.Error(), "slow_client_policy") {
		t.Fatalf("expected invalid slow client policy error, got %v", err)
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
