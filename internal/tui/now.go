package tui

import "time"

// now returns the current UTC time. It is a function variable so tests can
// override it.
var now = func() time.Time { return time.Now().UTC() }
