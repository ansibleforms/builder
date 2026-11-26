package registry

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHostAndInsecure checks that it names the registry by its host, and pushes to a plain http
// one without TLS.
func TestHostAndInsecure(t *testing.T) {
	if got := Host("https://registry.example.com/v2/"); got != "registry.example.com" {
		t.Errorf("Host = %s", got)
	}
	if !Insecure(&Login{URL: "http://localhost:5000"}) || Insecure(&Login{URL: "https://registry.example.com"}) || Insecure(nil) {
		t.Error("Insecure")
	}
}

// TestWriteDockerConfig checks that it writes a docker config only its owner reads.
func TestWriteDockerConfig(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteDockerConfig(dir, &Login{URL: "https://registry.example.com", User: "u", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.json")
	data, _ := os.ReadFile(file)
	var cfg struct {
		Auths map[string]struct{ Auth string }
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Auths["registry.example.com"].Auth != base64.StdEncoding.EncodeToString([]byte("u:p")) {
		t.Errorf("auths %v", cfg.Auths)
	}
	if st, _ := os.Stat(file); st.Mode().Perm()&0o077 != 0 {
		t.Errorf("mode %v", st.Mode())
	}
}

// TestWriteDockerConfigFails checks a folder that is not there.
func TestWriteDockerConfigFails(t *testing.T) {
	if _, err := WriteDockerConfig(filepath.Join(t.TempDir(), "none"), nil); err == nil {
		t.Error("written into no folder")
	}
}
