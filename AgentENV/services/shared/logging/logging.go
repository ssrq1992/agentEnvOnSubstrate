package logging

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func New(level string, format string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	resolvedFormat, err := resolveFormat(format, stdoutLooksInteractive())
	if err != nil {
		return nil, err
	}
	cfg.Encoding = resolvedFormat
	lvl := zapcore.InfoLevel
	_ = lvl.UnmarshalText([]byte(strings.ToLower(level)))
	cfg.Level = zap.NewAtomicLevelAt(lvl)
	return cfg.Build()
}

func resolveFormat(format string, interactive bool) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "auto":
		if interactive {
			return "console", nil
		}
		return "json", nil
	case "console", "json":
		return strings.ToLower(strings.TrimSpace(format)), nil
	default:
		return "", fmt.Errorf("unsupported log format %q", format)
	}
}

func stdoutLooksInteractive() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
