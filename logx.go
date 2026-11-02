// Package logx is structured (slog) logging for Gin services: lines carrying a
// request id, so one operation can be followed from the client through the server
// and into the database.
//
// What is logged is what happened, not the data: routes, ids, outcomes, durations,
// error text. Request and response bodies are never logged.
package logx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

type ctxKey struct{}

// Options configures Setup. Empty fields take the defaults shown.
type Options struct {
	// Service is added to every line as "service". Empty adds nothing.
	Service string
	// LevelEnv names the variable holding the level: debug, info (default), warn, error.
	// Default "LOG_LEVEL".
	LevelEnv string
	// FormatEnv names the variable holding the format: json (default), text, or gcp
	// (JSON with the field names Google Cloud Logging reads: severity, message).
	// Default "LOG_FORMAT".
	FormatEnv string
	// SQLEnv names the variable that, when "1", turns on LogSQL. Default "LOG_SQL".
	SQLEnv string
	// MetricPrefix is put before every metric name of DefaultMetrics. Default "".
	MetricPrefix string
	// Output is where lines are written. Default os.Stdout.
	Output io.Writer
}

// Setup installs the process-wide logger from the environment, as described by opts.
func Setup(opts Options) *slog.Logger {
	if opts.LevelEnv == "" {
		opts.LevelEnv = "LOG_LEVEL"
	}
	if opts.FormatEnv == "" {
		opts.FormatEnv = "LOG_FORMAT"
	}
	if opts.SQLEnv == "" {
		opts.SQLEnv = "LOG_SQL"
	}
	if opts.Output == nil {
		opts.Output = os.Stdout
	}
	LogSQL = os.Getenv(opts.SQLEnv) == "1"
	DefaultMetrics.SetPrefix(opts.MetricPrefix)
	return setup(opts.Output, os.Getenv(opts.LevelEnv), os.Getenv(opts.FormatEnv), opts.Service)
}

func setup(w io.Writer, level, format, service string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	if strings.EqualFold(format, "gcp") {
		// Cloud Logging takes the level from "severity" and the text from "message";
		// WARN is spelled WARNING there.
		opts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.LevelKey:
				level := strings.ToUpper(a.Value.String())
				if level == "WARN" {
					level = "WARNING"
				}
				return slog.String("severity", level)
			case slog.MessageKey:
				return slog.String("message", a.Value.String())
			}
			return a
		}
	}
	var h slog.Handler = slog.NewJSONHandler(w, opts)
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(w, opts)
	}
	l := slog.New(h)
	if service != "" {
		l = l.With("service", service)
	}
	slog.SetDefault(l)
	return l
}

// Into puts a logger on a context.
func Into(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From returns the logger of the request a context belongs to (with its request id
// and any annotations), or the process logger outside one.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
