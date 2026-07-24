package grok

import (
	"context"
	"testing"
	"time"
)

// testContext returns a context with a generous deadline for unit tests.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}
