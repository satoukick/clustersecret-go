// Package kubernetes contains helpers for namespace matching and Secret
// manipulation used by the ClusterSecret controller.
package kubernetes

import (
	"fmt"
	"regexp"
)

// MatchNamespace reports whether a namespace name matches any pattern in
// include and does NOT match any pattern in exclude. Exclude takes precedence:
// a namespace matched by both is rejected.
//
// Behavior:
//   - include is empty: never matches (returns false). This is intentional —
//     an empty include list means "no targets selected", not "all namespaces".
//     Users who want all namespaces should write include: [".*"].
//   - exclude is empty: only include determines the result.
//   - any pattern fails to compile: returns an error. Callers should surface
//     this in the ClusterSecret status so users see the broken regex
//     immediately, rather than the operator silently skipping namespaces.
func MatchNamespace(name string, include, exclude []string) (bool, error) {
	if len(include) == 0 {
		return false, nil
	}

	matched, err := anyMatch(name, include)
	if err != nil {
		return false, fmt.Errorf("matchNamespace: %w", err)
	}
	if !matched {
		return false, nil
	}

	excluded, err := anyMatch(name, exclude)
	if err != nil {
		return false, fmt.Errorf("avoidNamespaces: %w", err)
	}
	return !excluded, nil
}

// anyMatch returns true if name matches any of the given patterns.
// Each pattern is treated as Go's regexp; a pattern that fails to compile
// short-circuits with an error so misconfiguration is loud, not silent.
func anyMatch(name string, patterns []string) (bool, error) {
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return false, fmt.Errorf("invalid regex %q: %w", p, err)
		}
		if re.MatchString(name) {
			return true, nil
		}
	}
	return false, nil
}
