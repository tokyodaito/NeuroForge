package bootstrap

import "errors"

// errNotFound is the sentinel returned by FakeDetector when a tool is absent.
var errNotFound = errors.New("bootstrap: not found")
