package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

type Options struct {
	Component  string
	MaxSize    int
	MaxBackups int
	Level      string
}

func DefaultLogPath(component string) string {
	if runtime.GOOS == "windows" {
		return component + ".log"
	}
	return filepath.Join("/var/log/traptunnel", component+".log")
}

func ParseLevel(level string) slog.Level {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func Setup(opts Options) {
	logPath := DefaultLogPath(opts.Component)
	level := ParseLevel(opts.Level)

	if runtime.GOOS != "windows" {
		dir := filepath.Dir(logPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Printf("[!] 无法创建日志目录 %s: %v. 将仅输出到控制台。\n", dir, err)
			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
			slog.SetDefault(logger)
			return
		}
	}

	rotator := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    opts.MaxSize,
		MaxBackups: opts.MaxBackups,
		MaxAge:     28,
		Compress:   true,
	}

	writer := io.MultiWriter(os.Stdout, rotator)
	logger := slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{Key: "time", Value: slog.StringValue(attr.Value.Time().Format(time.RFC3339))}
			}
			return attr
		},
	}))
	slog.SetDefault(logger)
}
