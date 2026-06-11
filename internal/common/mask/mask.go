// Package mask redacts PII for audit logs and structured logging (DESIGN §11.5/§16.6).
package mask

// Name keeps only the first rune, masking the rest (e.g. 张** ).
func Name(s string) string {
	r := []rune(s)
	switch {
	case len(r) == 0:
		return ""
	case len(r) == 1:
		return string(r)
	default:
		out := string(r[0])
		for i := 1; i < len(r); i++ {
			out += "*"
		}
		return out
	}
}

// IDCard keeps the first 4 and last 2 chars (e.g. 3301**********12).
func IDCard(s string) string { return keepEnds(s, 4, 2) }

// Mobile keeps the first 3 and last 4 chars (e.g. 138****1009).
func Mobile(s string) string { return keepEnds(s, 3, 4) }

func keepEnds(s string, head, tail int) string {
	r := []rune(s)
	if len(r) <= head+tail {
		if len(r) == 0 {
			return ""
		}
		// too short to mask meaningfully; mask all but first.
		out := string(r[0])
		for i := 1; i < len(r); i++ {
			out += "*"
		}
		return out
	}
	out := string(r[:head])
	for i := head; i < len(r)-tail; i++ {
		out += "*"
	}
	out += string(r[len(r)-tail:])
	return out
}
