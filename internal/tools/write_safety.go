package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// classifyWritePath classifies a write/edit destination: AutoAllow inside
// the session root (and outside any "dangerous" path like /etc or ~/.ssh),
// Prompt anywhere else. An empty root means "no session" — every path
// classifies as Prompt.
func classifyWritePath(p, root string) Verdict {
	if p == "" || root == "" {
		return Prompt
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(absRoot, abs)
	}
	abs = filepath.Clean(abs)
	for _, d := range dangerousWritePaths() {
		if isUnder(abs, d) {
			return Prompt
		}
	}
	if isUnder(abs, absRoot) {
		return AutoAllow
	}
	return Prompt
}

// dangerousWritePaths lists locations where writes always prompt, even
// inside the session root. Aimed at credential/secret stores and system
// directories.
func dangerousWritePaths() []string {
	paths := []string{"/etc", "/boot", "/usr", "/bin", "/sbin", "/lib", "/lib64"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths,
			filepath.Join(home, ".ssh"),
			filepath.Join(home, ".aws"),
			filepath.Join(home, ".gnupg"),
			filepath.Join(home, ".config", "gcloud"),
			filepath.Join(home, ".kube"),
			filepath.Join(home, ".docker"),
			filepath.Join(home, ".netrc"),
		)
	}
	return paths
}

func isUnder(target, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
