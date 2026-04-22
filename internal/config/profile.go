package config

import (
	"fmt"
	"net"
	"slices"
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

func (p Profile) Valid() bool {
	switch p {
	case ProfileEdge, ProfileRelay, ProfileSink, ProfileFull:
		return true
	default:
		return false
	}
}

func (p Profile) Supports(capability string) bool {
	return slices.Contains(p.Capabilities(), capability)
}

func ApplyNodeDefaults(cfg *NodeConfig) {
	if cfg == nil {
		return
	}

	switch cfg.Profile {
	case ProfileEdge:
		cfg.Capture.Enabled = true
		cfg.Egress.Enabled = true
	case ProfileRelay:
		cfg.Capture.Enabled = true
		cfg.Ingress.Enabled = true
		cfg.Egress.Enabled = true
	case ProfileSink:
		cfg.Ingress.Enabled = true
		cfg.Inject.Enabled = true
	case ProfileFull:
		cfg.Capture.Enabled = true
		cfg.Ingress.Enabled = true
		cfg.Egress.Enabled = true
		cfg.Inject.Enabled = true
		cfg.Export.Enabled = true
	}

	if cfg.Capture.Enabled && len(cfg.Capture.ListenPorts) == 0 {
		cfg.Capture.ListenPorts = []int{162}
	}
	if cfg.Ingress.Enabled && cfg.Ingress.Listen == "" {
		cfg.Ingress.Listen = "0.0.0.0:10000"
	}
	if cfg.Egress.Enabled && cfg.Egress.ReconnectInterval <= 0 {
		cfg.Egress.ReconnectInterval = 5
	}
	if cfg.Inject.Enabled {
		if cfg.Inject.IP == "" {
			cfg.Inject.IP = "127.0.0.1"
		}
		if cfg.Inject.Port == 0 {
			cfg.Inject.Port = 162
		}
	}
	if cfg.Export.Enabled {
		if cfg.Export.Listen == "" {
			cfg.Export.Listen = "0.0.0.0:12000"
		}
		if cfg.Export.Format == "" {
			cfg.Export.Format = "frame"
		}
		if cfg.Export.MaxClients <= 0 {
			cfg.Export.MaxClients = 32
		}
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "INFO"
	}
	if cfg.Logging.MaxLogSize <= 0 {
		cfg.Logging.MaxLogSize = 10
	}
	if cfg.Logging.MaxLogBackups <= 0 {
		cfg.Logging.MaxLogBackups = 100
	}
}

func ValidateNode(cfg NodeConfig) error {
	if cfg.Profile == "" {
		return fmt.Errorf("node.profile is required")
	}
	if !cfg.Profile.Valid() {
		return fmt.Errorf("unsupported node.profile %q", cfg.Profile)
	}

	if err := validateCapability(cfg.Profile, "capture", cfg.Capture.Enabled); err != nil {
		return err
	}
	if err := validateCapability(cfg.Profile, "ingress", cfg.Ingress.Enabled); err != nil {
		return err
	}
	if err := validateCapability(cfg.Profile, "egress", cfg.Egress.Enabled); err != nil {
		return err
	}
	if err := validateCapability(cfg.Profile, "inject", cfg.Inject.Enabled); err != nil {
		return err
	}
	if err := validateCapability(cfg.Profile, "export", cfg.Export.Enabled); err != nil {
		return err
	}

	if cfg.Capture.Enabled {
		if cfg.ID == 0 {
			return fmt.Errorf("node.id is required when capture is enabled")
		}
		if len(cfg.Capture.ListenPorts) == 0 {
			return fmt.Errorf("capture.listen_ports must not be empty when capture is enabled")
		}
		for _, port := range cfg.Capture.ListenPorts {
			if err := validatePort("capture.listen_ports", port); err != nil {
				return err
			}
		}
	}

	if cfg.Ingress.Enabled && !validListenAddress(cfg.Ingress.Listen) {
		return fmt.Errorf("ingress.listen must be a valid host:port, got %q", cfg.Ingress.Listen)
	}

	if cfg.Egress.Enabled {
		if cfg.ID == 0 {
			return fmt.Errorf("node.id is required when egress is enabled")
		}
		if cfg.Egress.ReconnectInterval <= 0 {
			return fmt.Errorf("egress.reconnect_interval must be greater than 0")
		}
		if len(cfg.Egress.Groups) == 0 {
			return fmt.Errorf("egress.groups must not be empty when egress is enabled")
		}
		for groupIndex, group := range cfg.Egress.Groups {
			if len(group.Members) == 0 {
				return fmt.Errorf("egress.groups[%d].members must not be empty", groupIndex)
			}
			for memberIndex, member := range group.Members {
				if !validListenAddress(member) {
					return fmt.Errorf("egress.groups[%d].members[%d] must be a valid host:port, got %q", groupIndex, memberIndex, member)
				}
			}
		}
	}

	if cfg.Inject.Enabled {
		if ip := net.ParseIP(cfg.Inject.IP); ip == nil {
			return fmt.Errorf("inject.ip must be a valid IP address, got %q", cfg.Inject.IP)
		}
		if err := validatePort("inject.port", cfg.Inject.Port); err != nil {
			return err
		}
	}
	if cfg.Inject.SNMPv1AgentAddrOverride && !cfg.Inject.Enabled {
		return fmt.Errorf("inject.snmpv1_agent_addr_override requires inject.enabled")
	}

	if cfg.Export.Enabled {
		if !validListenAddress(cfg.Export.Listen) {
			return fmt.Errorf("export.listen must be a valid host:port, got %q", cfg.Export.Listen)
		}
		if cfg.Export.Format != "frame" {
			return fmt.Errorf("export.format must be %q, got %q", "frame", cfg.Export.Format)
		}
		if cfg.Export.MaxClients <= 0 {
			return fmt.Errorf("export.max_clients must be greater than 0")
		}
	}

	if cfg.Inject.Enabled && cfg.Capture.Enabled {
		for _, port := range cfg.Capture.ListenPorts {
			if port == cfg.Inject.Port {
				return fmt.Errorf("inject.port %d conflicts with capture.listen_ports", cfg.Inject.Port)
			}
		}
	}
	return nil
}

func validateCapability(profile Profile, capability string, enabled bool) error {
	if !enabled {
		if profile.Supports(capability) {
			return fmt.Errorf("profile %q requires %s.enabled", profile, capability)
		}
		return nil
	}
	if !profile.Supports(capability) {
		return fmt.Errorf("profile %q does not support %s.enabled", profile, capability)
	}
	return nil
}

func validatePort(field string, port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("%s must be between 1 and 65535, got %d", field, port)
	}
	return nil
}

func validListenAddress(addr string) bool {
	if addr == "" {
		return false
	}
	_, _, err := net.SplitHostPort(addr)
	return err == nil
}
