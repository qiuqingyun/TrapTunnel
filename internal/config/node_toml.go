package config

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

type nodeFile struct {
	Node    nodeSection   `toml:"node"`
	Capture CaptureConfig `toml:"capture"`
	Ingress IngressConfig `toml:"ingress"`
	Egress  EgressConfig  `toml:"egress"`
	Inject  InjectConfig  `toml:"inject"`
	Export  ExportConfig  `toml:"export"`
	Logging LoggingConfig `toml:"logging"`
}

type nodeSection struct {
	ID      uint16  `toml:"id"`
	Name    string  `toml:"name"`
	Profile Profile `toml:"profile"`
}

func LoadNode(path string) (NodeConfig, error) {
	var file nodeFile

	meta, err := toml.DecodeFile(path, &file)
	if err != nil {
		return NodeConfig{}, fmt.Errorf("load node config %q: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return NodeConfig{}, fmt.Errorf("load node config %q: unknown fields %v", path, undecoded)
	}

	cfg := NodeConfig{
		ID:      file.Node.ID,
		Name:    file.Node.Name,
		Profile: file.Node.Profile,
		Capture: file.Capture,
		Ingress: file.Ingress,
		Egress:  file.Egress,
		Inject:  file.Inject,
		Export:  file.Export,
		Logging: file.Logging,
	}

	ApplyNodeDefaults(&cfg)
	if err := ValidateNode(cfg); err != nil {
		return NodeConfig{}, fmt.Errorf("load node config %q: %w", path, err)
	}
	return cfg, nil
}
