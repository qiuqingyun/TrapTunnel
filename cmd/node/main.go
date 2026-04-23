package main

import (
	"flag"
	"os"
	nodeapp "traptunnel/internal/node"
)

func main() {
	configPath := flag.String("c", "node.toml", "Path to node config file")
	flag.Parse()

	os.Exit(nodeapp.MainWithConfigPath(*configPath))
}
