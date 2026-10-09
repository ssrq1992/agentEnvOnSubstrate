package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationRejectsUnknownAndTrailingObjects(t *testing.T) {
	for _, body := range []string{`{}`, `{"listen":":8443","unknown":true}`, `{"listen":":8443"} {}`, `{"listen":":8443"}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readConfig(path)
		if (err == nil) != (body == `{"listen":":8443"}`) {
			t.Fatalf("unexpected config result for %s: %v", body, err)
		}
	}
}
