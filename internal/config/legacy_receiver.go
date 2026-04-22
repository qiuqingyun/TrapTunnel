package config

import (
	"strconv"

	"gopkg.in/ini.v1"
)

type ReceiverLegacyConfig struct {
	ListenPort    string
	InjectIP      string
	NodeMapping   map[uint16]string
	MaxLogSize    int
	MaxLogBackups int
	LogLevel      string
}

func LoadReceiverLegacy(path string, previous *ReceiverLegacyConfig) (ReceiverLegacyConfig, error) {
	cfgFile, err := ini.Load(path)
	if err != nil {
		return ReceiverLegacyConfig{}, err
	}

	cfg := ReceiverLegacyConfig{
		ListenPort:    "10000",
		InjectIP:      "127.0.0.1",
		NodeMapping:   make(map[uint16]string),
		MaxLogSize:    10,
		MaxLogBackups: 100,
		LogLevel:      "INFO",
	}
	if previous != nil {
		for id, name := range previous.NodeMapping {
			cfg.NodeMapping[id] = name
		}
	}

	sectionServer := cfgFile.Section("server")
	cfg.ListenPort = sectionServer.Key("listen_port").MustString(cfg.ListenPort)
	cfg.InjectIP = sectionServer.Key("inject_ip").MustString(cfg.InjectIP)

	sectionLogging := cfgFile.Section("logging")
	cfg.MaxLogSize = sectionLogging.Key("max_log_size").MustInt(cfg.MaxLogSize)
	cfg.MaxLogBackups = sectionLogging.Key("max_log_backups").MustInt(cfg.MaxLogBackups)
	cfg.LogLevel = sectionLogging.Key("log_level").MustString(cfg.LogLevel)

	sectionNodes := cfgFile.Section("nodes")
	for _, key := range sectionNodes.Keys() {
		if id, err := strconv.ParseUint(key.Name(), 10, 16); err == nil {
			cfg.NodeMapping[uint16(id)] = key.String()
		}
	}

	return cfg, nil
}
