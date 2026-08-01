package momenu

import (
	"encoding/json"
	"strings"
)

// httpxFirst devolve o primeiro valor não vazio.
func httpxFirst(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
