package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"sleepy/internal/logx"
)

// IsWhitelisted reads whitelist.json on every call so /whitelist add takes effect immediately.
func IsWhitelisted(directory, username string) bool {
	path := filepath.Join(directory, "whitelist.json")
	content, err := os.ReadFile(path)
	if err != nil {
		logx.Printf("Cannot read %s (%v): nobody can wake the server", path, err)
		return false
	}
	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(content, &entries); err != nil {
		logx.Printf("%s is not a valid whitelist (%v): nobody can wake the server", path, err)
		return false
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, username) {
			return true
		}
	}
	return false
}
