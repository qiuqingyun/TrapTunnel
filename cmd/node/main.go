package main

import (
	"flag"
	"log/slog"
	"os"

	"traptunnel/internal/config"
	"traptunnel/internal/logging"
	nodeapp "traptunnel/internal/node"
)

func main() {
	configPath := flag.String("c", "node.toml", "Path to node config file")
	flag.Parse()

	cfg, err := config.LoadNode(*configPath)
	if err != nil {
		slog.Error("加载 node 配置失败", "error", err)
		os.Exit(1)
	}

	logging.Setup(logging.Options{
		Component:  "node",
		MaxSize:    cfg.Logging.MaxLogSize,
		MaxBackups: cfg.Logging.MaxLogBackups,
		Level:      cfg.Logging.Level,
	})

	os.Exit(nodeapp.Main(cfg))
}
