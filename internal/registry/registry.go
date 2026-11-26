// Package registry writes the registry login a build pushes with: a docker config.json of its
// own, in the build's temporary folder, read by BuildKit's session and by cosign (DOCKER_CONFIG),
// removed with the folder.
package registry

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Login is the registry credential a build comes with, sealed for this builder.
type Login struct {
	// URL is the registry's address (https://registry.example.com, http://localhost:5000).
	URL string `json:"url"`
	// User is the user it signs in as.
	User string `json:"user"`
	// Password is its password or token.
	Password string `json:"password"`
}

var (
	scheme = regexp.MustCompile(`(?i)^[a-z]+://`)
	plain  = regexp.MustCompile(`(?i)^http://`)
)

// Host is the registry's host, as a docker config names it: its address without scheme nor
// path.
//
// Args:
//
//	url: the registry credential's address (https://registry.example.com/v2/).
//
// Returns:
//
//	The host (registry.example.com).
func Host(url string) string {
	h := scheme.ReplaceAllString(url, "")
	if i := strings.Index(h, "/"); i >= 0 {
		h = h[:i]
	}
	return h
}

// WriteDockerConfig writes the docker config with the registry's login, only its owner reading
// it.
//
// Args:
//
//	dir: the folder it goes in.
//	login: the registry's login ; nil or without an address : a config without logins.
//
// Returns:
//
//	The folder (DOCKER_CONFIG).
//
// Errors:
//
//	When the file cannot be written.
func WriteDockerConfig(dir string, login *Login) (string, error) {
	type auth struct {
		Auth string `json:"auth"`
	}
	auths := map[string]auth{}
	if login != nil && login.URL != "" {
		auths[Host(login.URL)] = auth{base64.StdEncoding.EncodeToString([]byte(login.User + ":" + login.Password))}
	}
	// a map of strings : json.Marshal has no error to give
	data, _ := json.Marshal(map[string]any{"auths": auths})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// Insecure tells whether a registry is reached over plain http (its address says http://):
// pushed to without TLS.
//
// Args:
//
//	login: the registry's login, nil for none.
//
// Returns:
//
//	True for an http:// address.
func Insecure(login *Login) bool {
	return login != nil && plain.MatchString(login.URL)
}
