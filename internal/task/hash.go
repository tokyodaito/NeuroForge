package task

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// hashAndCount reads from r, computing the SHA-256 hash of the content and
// counting the total bytes. It returns the hex-encoded hash and byte count.
// The reader is consumed but not rewound.
func hashAndCount(r io.Reader) (hash string, size int64, err error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
