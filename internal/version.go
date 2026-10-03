package buzz

import (
	"os"
	"strings"
)

// Version holds the current application version, injected via ldflags during build
// or loaded from VERSION file at runtime during development.
var Version = "dev"

func init() {
	if Version == "dev" {
		if data, err := os.ReadFile("VERSION"); err == nil {
			if v := strings.TrimSpace(string(data)); v != "" {
				Version = v
			}
		}
	}
}
