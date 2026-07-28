package storage

import (
	"encoding/json"
	"time"
)

// jsonDecode is the package-local indirection over encoding/json used by the
// Work Graph substrate (workgraph.go). Declaring it here keeps the import
// surface narrow and lets a future swap (e.g. to a streaming decoder) touch
// one place.
func jsonDecode(data []byte, out any) error {
	return json.Unmarshal(data, out)
}

// jsonEncode is the symmetric encoder used by the Work Graph substrate.
func jsonEncode(v any) ([]byte, error) {
	return json.Marshal(v)
}

// utcNowRFC3339 returns the current UTC time as an RFC3339Nano string. It is
// the package-local time-formatting helper used by lease-expiry comparisons
// so every site uses the same clock shape (a single timestamp string is
// compared lexicographically against expires_at, which is itself RFC3339Nano
// so the comparison is correct).
func utcNowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
