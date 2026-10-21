package backend

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Paper and its forks (Purpur, Folia...) keep proxies.proxy-protocol in this file.
const paperGlobalConfig = "config/paper-global.yml"

// ConfigureProxyProtocol aligns the server with mode ("auto", "on", "off") before a start and
// reports whether Sleepy must send the PROXY header to this run. Both sides must agree: a header
// the server does not expect, or a missing one it does, makes every connection fail.
func ConfigureProxyProtocol(directory, mode string) (active, changed bool, err error) {
	path := filepath.Join(directory, paperGlobalConfig)
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// Not Paper (or its first start, before the file exists): only forced "on" sends it.
		return mode == "on", false, nil
	}
	if err != nil {
		return false, false, err
	}
	want := mode != "off"
	updated := setPaperProxyProtocol(string(content), want)
	if updated == string(content) {
		return want, false, nil
	}
	return want, true, os.WriteFile(path, []byte(updated), 0o644)
}

var proxyProtocolLine = regexp.MustCompile(`^proxy-protocol:`)

// setPaperProxyProtocol sets proxies.proxy-protocol in paper-global.yml, touching nothing else.
func setPaperProxyProtocol(content string, value bool) string {
	eol := "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	setting := "proxy-protocol: " + strconv.FormatBool(value)

	proxies := -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t") == "proxies:" {
			proxies = i
			break
		}
	}
	if proxies < 0 {
		at := len(lines)
		if at > 0 && lines[at-1] == "" {
			at-- // keep the trailing newline last
		}
		lines = insert(lines, at, "proxies:", "  "+setting)
		return strings.Join(lines, eol)
	}

	// Look for the key among the direct children of "proxies:".
	childIndent := ""
	for j := proxies + 1; j < len(lines); j++ {
		trimmed := strings.TrimSpace(lines[j])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := lines[j][:len(lines[j])-len(strings.TrimLeft(lines[j], " "))]
		if indent == "" {
			break // end of the proxies block
		}
		if childIndent == "" {
			childIndent = indent
		}
		if indent == childIndent && proxyProtocolLine.MatchString(trimmed) {
			lines[j] = childIndent + setting
			return strings.Join(lines, eol)
		}
	}
	if childIndent == "" {
		childIndent = "  "
	}
	lines = insert(lines, proxies+1, childIndent+setting)
	return strings.Join(lines, eol)
}

func insert(lines []string, at int, add ...string) []string {
	out := append([]string{}, lines[:at]...)
	out = append(out, add...)
	return append(out, lines[at:]...)
}
