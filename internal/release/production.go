package release

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrInvalidProductionInput = errors.New("生产发布输入无效")
	ErrInvalidSSHArguments    = errors.New("生产 SSH 参数无效")
	productionTargetPattern   = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	sshHostPattern            = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

type ProductionInput struct {
	Operation string
	TargetID  string
}

func ValidateProductionInput(input ProductionInput) error {
	switch input.Operation {
	case "deploy":
		if !productionTargetPattern.MatchString(input.TargetID) {
			return ErrInvalidProductionInput
		}
	case "rollback":
		if input.TargetID != "bootstrap" && !productionTargetPattern.MatchString(input.TargetID) {
			return ErrInvalidProductionInput
		}
	default:
		return ErrInvalidProductionInput
	}
	return nil
}

func SSHArguments(host, identityFile, knownHostsFile string) ([]string, error) {
	if !sshHostPattern.MatchString(host) || !safeAbsolutePath(identityFile) || !safeAbsolutePath(knownHostsFile) ||
		filepath.Clean(identityFile) == filepath.Clean(knownHostsFile) {
		return nil, ErrInvalidSSHArguments
	}
	return []string{
		"-i", identityFile,
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHostsFile,
		"-o", "PasswordAuthentication=no",
		"--", "yunling-deploy@" + host, "execute",
	}, nil
}

func safeAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && !strings.ContainsAny(path, "\x00\r\n")
}
