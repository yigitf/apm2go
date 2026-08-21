package attach

import "strings"

// parseProperties decodes the target's reply to the "properties" command,
// which is a java.util.Properties dump: one key=value per line, with comment
// lines starting with '#' and backslash escapes for special characters.
func parseProperties(body string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		out[strings.TrimSpace(key)] = unescapeProperty(value)
	}
	return out
}

// unescapeProperty resolves the backslash escapes java.util.Properties writes.
func unescapeProperty(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'f':
			b.WriteByte('\f')
		default:
			// Covers the escaped ':', '=', ' ' and '\' cases.
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
