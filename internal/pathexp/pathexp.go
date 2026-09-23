package pathexp

import (
	"os"
	"path/filepath"
	"strings"
)

func Home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

func Expand(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		return Home()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(Home(), strings.TrimPrefix(p, "~/"))
	}
	return p
}