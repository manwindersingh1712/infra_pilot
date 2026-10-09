// Package logging builds the process logger: JSON in production, text in dev.
package logging

import (
	"io"
	"log/slog"
)

func New(format string, w io.Writer) *slog.Logger {
	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, nil))
	}
	return slog.New(slog.NewTextHandler(w, nil))
}
