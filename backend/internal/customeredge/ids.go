package customeredge

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// FormatUUIDToSMPPHex converts a message UUID into 32 lowercase hex characters (without hyphens).
func FormatUUIDToSMPPHex(id uuid.UUID) string {
	raw := id[:]
	return hex.EncodeToString(raw)
}

// ParseMessageID parses an ID string that may be formatted either as a standard 36-char hyphenated
// UUID or as a 32-character lowercase hex string.
func ParseMessageID(raw string) (uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 36 {
		return uuid.Parse(raw)
	}

	if len(raw) == 32 {
		bytes, err := hex.DecodeString(raw)
		if err != nil {
			return uuid.Nil, fmt.Errorf("invalid 32-hex message id: %w", err)
		}
		return uuid.FromBytes(bytes)
	}

	return uuid.Nil, fmt.Errorf("invalid message id format: expected 36-char uuid or 32-char hex, got length %d", len(raw))
}
