package scheduler

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

// newOwnerID generates a per-process lease-owner identifier: unique enough to
// tell two processes (or a crashed process and its replacement) apart, and
// readable enough to be a useful label on the self-observability metrics
// ADR-0001's Consequences section names (a gauge of currently-held leases per
// process, labeled by owner).
func newOwnerID() (string, error) {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown-host"
	}

	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate owner id: %w", err)
	}

	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(buf)), nil
}
