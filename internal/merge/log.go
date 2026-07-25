package merge

import (
	"io"
	"log/slog"
)

// discardHandler returns a text handler that drops all output, for callers that
// do not inject a logger.
func discardHandler() slog.Handler {
	return slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1})
}
