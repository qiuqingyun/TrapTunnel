package config

import (
	"fmt"
)

type Profile string

const (
	ProfileEdge  Profile = "edge"
	ProfileRelay Profile = "relay"
	ProfileSink  Profile = "sink"
	ProfileFull  Profile = "full"
)

type NodeConfig struct {
	ID      uint16        `toml:"id"`
	Name    string        `toml:"name"`
	Profile Profile       `toml:"profile"`
	Capture CaptureConfig `toml:"capture"`
	Ingress IngressConfig `toml:"ingress"`
	Egress  EgressConfig  `toml:"egress"`
	Inject  InjectConfig  `toml:"inject"`
	Export  ExportConfig  `toml:"export"`
	Logging LoggingConfig `toml:"logging"`
}

type CaptureConfig struct {
	Enabled     bool  `toml:"enabled"`
	ListenPorts []int `toml:"listen_ports"`
}

type IngressConfig struct {
	Enabled bool   `toml:"enabled"`
	Listen  string `toml:"listen"`
}

type EgressConfig struct {
	Enabled           bool          `toml:"enabled"`
	ReconnectInterval int           `toml:"reconnect_interval"`
	Groups            []EgressGroup `toml:"groups"`
}

type EgressGroup struct {
	Members []string `toml:"members"`
}

type InjectConfig struct {
	Enabled                 bool   `toml:"enabled"`
	IP                      string `toml:"ip"`
	Port                    int    `toml:"port"`
	SNMPv1AgentAddrOverride bool   `toml:"snmpv1_agent_addr_override"`
}

type ExportConfig struct {
	Enabled    bool   `toml:"enabled"`
	Listen     string `toml:"listen"`
	Format     string `toml:"format"`
	MaxClients int    `toml:"max_clients"`
}

type LoggingConfig struct {
	Level         string `toml:"level"`
	MaxLogSize    int    `toml:"max_log_size"`
	MaxLogBackups int    `toml:"max_log_backups"`
}

func (p Profile) Capabilities() []string {
	switch p {
	case ProfileEdge:
		return []string{"capture", "egress"}
	case ProfileRelay:
		return []string{"capture", "ingress", "egress"}
	case ProfileSink:
		return []string{"ingress", "inject"}
	case ProfileFull:
		return []string{"capture", "ingress", "egress", "inject", "export"}
	default:
		return nil
	}
}

func ValidateNode(cfg NodeConfig) error {
	if cfg.Inject.Enabled && cfg.Capture.Enabled {
		for _, port := range cfg.Capture.ListenPorts {
			if port == cfg.Inject.Port {
				return fmt.Errorf("inject.port %d conflicts with capture.listen_ports", cfg.Inject.Port)
			}
		}
	}
	return nil
}
