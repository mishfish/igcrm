package tgbot

import (
	"fmt"
	"strconv"
	"strings"
)

// Allowlist is the set of Telegram user IDs permitted to use the bot.
type Allowlist map[int64]bool

// ParseAllowlist parses a comma-separated list of Telegram user IDs.
// Whitespace around each entry is ignored. Any non-numeric entry is an error.
func ParseAllowlist(csv string) (Allowlist, error) {
	a := Allowlist{}
	for _, raw := range strings.Split(csv, ",") {
		entry := strings.TrimSpace(raw)
		id, err := strconv.ParseInt(entry, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("allowlist: invalid user id %q: %w", entry, err)
		}
		a[id] = true
	}
	return a, nil
}

// Allowed reports whether userID is on the allowlist.
func (a Allowlist) Allowed(userID int64) bool {
	return a[userID]
}
