package config

import (
	"net"
	"strings"

	"gopkg.in/ini.v1"
)

type SenderLegacyConfig struct {
	NodeID            uint16
	Servers           []string
	ListenPort        int
	ReconnectInterval int
	MaxBufferSize     int
	MaxLogSize        int
	MaxLogBackups     int
	LogLevel          string
}

func LoadSenderLegacy(path string, previous *SenderLegacyConfig, firstRun bool) (SenderLegacyConfig, error) {
	cfgFile, err := ini.Load(path)
	if err != nil {
		return SenderLegacyConfig{}, err
	}

	var cfg SenderLegacyConfig
	if previous != nil {
		cfg = *previous
		cfg.Servers = []string{}
	} else {
		cfg = SenderLegacyConfig{
			ReconnectInterval: 5,
			MaxBufferSize:     2000,
			MaxLogSize:        10,
			MaxLogBackups:     100,
			LogLevel:          "INFO",
		}
	}

	sectionCommon := cfgFile.Section("common")
	if k, err := sectionCommon.GetKey("node_id"); err == nil {
		if value, err := k.Int(); err == nil {
			cfg.NodeID = uint16(value)
		}
	}
	if k, err := sectionCommon.GetKey("servers"); err == nil {
		servers := strings.Split(k.String(), ",")
		for _, server := range servers {
			server = strings.TrimSpace(server)
			if server != "" {
				cfg.Servers = append(cfg.Servers, server)
			}
		}
	}

	legacyServerIP := sectionCommon.Key("b_server_ip").String()
	legacyServerPort := sectionCommon.Key("b_server_port").String()

	sectionAdvanced := cfgFile.Section("advanced")
	cfg.ListenPort = sectionAdvanced.Key("listen_port").MustInt(cfg.ListenPort)
	cfg.ReconnectInterval = sectionAdvanced.Key("reconnect_interval").MustInt(cfg.ReconnectInterval)
	if firstRun || previous == nil {
		cfg.MaxBufferSize = sectionAdvanced.Key("max_buffer_size").MustInt(cfg.MaxBufferSize)
	}

	sectionLogging := cfgFile.Section("logging")
	cfg.MaxLogSize = sectionLogging.Key("max_log_size").MustInt(cfg.MaxLogSize)
	cfg.MaxLogBackups = sectionLogging.Key("max_log_backups").MustInt(cfg.MaxLogBackups)
	cfg.LogLevel = sectionLogging.Key("log_level").MustString(cfg.LogLevel)

	if len(cfg.Servers) == 0 && legacyServerIP != "" && legacyServerPort != "" {
		cfg.Servers = append(cfg.Servers, net.JoinHostPort(legacyServerIP, legacyServerPort))
	}

	return cfg, nil
}
