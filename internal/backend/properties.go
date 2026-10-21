package backend

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const propertiesFile = "server.properties"

// RewriteServerProperties points the server at 127.0.0.1:<port>. Every other line is left untouched.
func RewriteServerProperties(directory string, port int) error {
	path := filepath.Join(directory, propertiesFile)
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated := SetProperties(string(original), [][2]string{{"server-port", strconv.Itoa(port)}, {"server-ip", "127.0.0.1"}})
	if updated == string(original) {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

// ReadMaxPlayers returns max-players from server.properties, 20 if absent.
func ReadMaxPlayers(directory string) int {
	content, err := os.ReadFile(filepath.Join(directory, propertiesFile))
	if err != nil {
		return 20
	}
	v, ok := GetProperty(string(content), "max-players")
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if !ok || err != nil || n < 0 {
		return 20
	}
	return n
}

// ReadMotd returns motd= from server.properties, unescaped (§ becomes §, \n a new line).
func ReadMotd(directory string) string {
	content, err := os.ReadFile(filepath.Join(directory, propertiesFile))
	if err != nil {
		return ""
	}
	v, _ := GetProperty(string(content), "motd")
	return unescapeProperty(v)
}

// unescapeProperty decodes java.util.Properties escapes: \uXXXX, \n, \t, \r, \f and \<char>.
func unescapeProperty(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' || i+1 >= len(v) {
			b.WriteByte(v[i])
			continue
		}
		i++
		switch v[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'f':
			b.WriteByte('\f')
		case 'u':
			if i+4 < len(v) {
				if n, err := strconv.ParseUint(v[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(n))
					i += 4
					break
				}
			}
			b.WriteByte('u')
		default:
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

// keyLine matches "key=value", "key = value" and "key:value" (java.util.Properties syntax).
var keyLine = regexp.MustCompile(`^\s*([^#!\s=:][^\s=:]*)\s*[=:\s]\s*(.*)$`)

func splitLines(content string) []string {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// GetProperty returns the last occurrence of key, like java.util.Properties.
func GetProperty(content, key string) (value string, found bool) {
	for _, line := range splitLines(content) {
		if m := keyLine.FindStringSubmatch(line); m != nil && m[1] == key {
			value, found = m[2], true
		}
	}
	return value, found
}

// SetProperties sets keys in place (every occurrence), appends missing ones and keeps the
// file's line endings.
func SetProperties(content string, values [][2]string) string {
	eol := "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
	}
	var lines []string
	if content != "" {
		lines = splitLines(content)
	}
	found := map[string]bool{}
	for i, line := range lines {
		m := keyLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		for _, kv := range values {
			if m[1] == kv[0] {
				lines[i] = kv[0] + "=" + kv[1]
				found[kv[0]] = true
			}
		}
	}
	// Keep the final newline after the appended keys.
	endsWithNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if endsWithNewline {
		lines = lines[:len(lines)-1]
	}
	for _, kv := range values {
		if !found[kv[0]] {
			lines = append(lines, kv[0]+"="+kv[1])
		}
	}
	out := strings.Join(lines, eol)
	if endsWithNewline || content == "" {
		out += eol
	}
	return out
}
