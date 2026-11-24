// Package observability configures structured logging.
package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a JSON slog logger at the given level (debug, info, warn, error).
func NewLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})).With("service", "ledgerflow")
}
