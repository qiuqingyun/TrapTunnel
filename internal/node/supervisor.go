package node

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"traptunnel/internal/config"
	"traptunnel/internal/logging"
)

type configLoader func(string) (config.NodeConfig, error)
type runtimeRunner func(context.Context, config.NodeConfig) error
type loggingApplier func(config.NodeConfig)

// MainWithConfigPath starts the node runtime and handles SIGHUP reloads.
func MainWithConfigPath(configPath string) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	return supervise(configPath, config.LoadNode, applyLogging, Run, signals)
}

func applyLogging(cfg config.NodeConfig) {
	logging.Setup(logging.Options{
		Component:  "node",
		MaxSize:    cfg.Logging.MaxLogSize,
		MaxBackups: cfg.Logging.MaxLogBackups,
		Level:      cfg.Logging.Level,
	})
}

func supervise(configPath string, load configLoader, setupLogging loggingApplier, run runtimeRunner, signals <-chan os.Signal) int {
	cfg, err := load(configPath)
	if err != nil {
		slog.Error("加载 node 配置失败", "error", err)
		return 1
	}
	setupLogging(cfg)

	currentCtx, cancelCurrent := context.WithCancel(context.Background())
	currentErrCh := startRuntime(run, currentCtx, cfg)

	for {
		select {
		case sig := <-signals:
			switch sig {
			case syscall.SIGHUP:
				reloaded, err := load(configPath)
				if err != nil {
					slog.Error("SIGHUP 重载失败，继续使用当前配置", "error", err)
					continue
				}

				setupLogging(reloaded)
				slog.Info("收到 SIGHUP，开始重载 node 配置", "config_path", configPath)

				cancelCurrent()
				runtimeErr := <-currentErrCh
				if runtimeErr != nil && !errors.Is(runtimeErr, context.Canceled) {
					slog.Error("旧 runtime 退出异常", "error", runtimeErr)
				}

				currentCtx, cancelCurrent = context.WithCancel(context.Background())
				currentErrCh = startRuntime(run, currentCtx, reloaded)
				cfg = reloaded
				slog.Info("node 配置重载完成", "profile", cfg.Profile, "config_path", configPath)
			case os.Interrupt, syscall.SIGTERM:
				cancelCurrent()
				runtimeErr := <-currentErrCh
				if runtimeErr != nil && !errors.Is(runtimeErr, context.Canceled) {
					slog.Error("Node 退出异常", "error", runtimeErr)
					return 1
				}
				return 0
			}
		case err := <-currentErrCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("Node 运行失败", "component", "node", "profile", cfg.Profile, "error", err)
				return 1
			}
			return 0
		}
	}
}

func startRuntime(run runtimeRunner, ctx context.Context, cfg config.NodeConfig) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg)
	}()
	return errCh
}
