package agentprotocol

import (
	"errors"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxArtifactFileBytes  int64 = 100 << 20
	MaxArtifactTotalBytes int64 = 500 << 20
	MaxArtifactFiles            = 100
)

// ArtifactPolicy only matches ordinary files directly inside one run's private
// working directory. Nested paths and hidden agent metadata are never collected.
type ArtifactPolicy struct {
	AllowedGlobs  []string `json:"allowedGlobs"`
	MaxFileBytes  int64    `json:"maxFileBytes"`
	MaxTotalBytes int64    `json:"maxTotalBytes"`
}

func (p *ArtifactPolicy) Validate() error {
	if p == nil {
		return nil
	}
	invalid := errors.New("产物策略无效：请设置文件名白名单、单文件上限和总大小上限")
	if len(p.AllowedGlobs) == 0 || len(p.AllowedGlobs) > 20 || p.MaxFileBytes <= 0 || p.MaxFileBytes > MaxArtifactFileBytes || p.MaxTotalBytes < p.MaxFileBytes || p.MaxTotalBytes > MaxArtifactTotalBytes {
		return invalid
	}
	for _, pattern := range p.AllowedGlobs {
		if pattern == "" || len(pattern) > 128 || pattern != strings.TrimSpace(pattern) || strings.ContainsAny(pattern, "/\\\x00\r\n") || strings.HasPrefix(pattern, ".") {
			return invalid
		}
		if _, err := path.Match(pattern, "probe.csv"); err != nil {
			return invalid
		}
	}
	return nil
}

func (p *ArtifactPolicy) Allows(name string) bool {
	if p == nil || name == "" || len(name) > 255 || !utf8.ValidString(name) || name == "." || name == ".." || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	switch name {
	case "systemd-run-spec.json", "systemd-exit-code", "stdout.log", "stderr.log":
		return false
	}
	for _, pattern := range p.AllowedGlobs {
		if matched, err := path.Match(pattern, name); err == nil && matched {
			return true
		}
	}
	return false
}
