package keypouch

import (
	"encoding/base64"
	"strings"
)

// KeyPouch deep links only choose a local action. The site, record and instance
// details come from the configured HTTPS origin after pairing.
func ParseLink(raw string) (action, ticket string, ok bool) {
	const prefix = "ant-browser://keypouch/"
	path, ok := strings.CutPrefix(raw, prefix)
	if !ok {
		return "", "", false
	}
	action, ticket, ok = strings.Cut(path, "/")
	if !ok || (action != "pair" && action != "create-open") || len(ticket) != 32 {
		return "", "", false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(ticket)
	if err != nil || len(b) != 24 {
		return "", "", false
	}
	return action, ticket, true
}
