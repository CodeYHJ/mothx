package agentruntime

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// NormalizeAdditionalDirectories validates the ACP directory-root contract:
// absolute, cleaned, deterministic and duplicate-free paths.
func NormalizeAdditionalDirectories(directories []string) ([]string, error) {
	seen := make(map[string]struct{}, len(directories))
	result := make([]string, 0, len(directories))
	for _, raw := range directories {
		value := strings.TrimSpace(raw)
		if value == "" || !filepath.IsAbs(value) {
			return nil, fmt.Errorf("additional directory must be an absolute path: %q", raw)
		}
		value = filepath.Clean(value)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}
