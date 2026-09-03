package release

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrUnsafeBuildSource = errors.New("Docker 构建来源不安全")
	pinnedBasePattern    = regexp.MustCompile(`^[^@[:space:]]+@sha256:[0-9a-f]{64}$`)
)

var protectedBuildContextPaths = []string{
	".git/config", ".worktrees/probe", ".tools/probe", "node_modules/probe",
	"apps/web/dist/probe", "bin/probe", "coverage/probe", "work/probe", "outputs/probe",
	"deploy/agent/yunling-agent-0.1.0-linux-amd64.tar.gz",
	".env", "nested/.env", "deploy/.env", "deploy/secrets/key",
	"releases/current.json", "nested/releases/current.json",
	"backups/database.dump", "nested/backups/database.dump",
	"server.pem", "nested/server.pem", "id_rsa", "nested/id_rsa_deploy",
	"id_ed25519", "nested/id_ed25519_deploy",
}

func ValidateBuildSources(repositoryRoot string) error {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil || repositoryRoot == "" {
		return fmt.Errorf("%w：仓库根目录无效", ErrUnsafeBuildSource)
	}
	deployPath := filepath.Join(root, "deploy")
	entries, err := os.ReadDir(deployPath)
	if err != nil {
		return fmt.Errorf("%w：读取 deploy 目录：%v", ErrUnsafeBuildSource, err)
	}
	dockerfileCount := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "Dockerfile") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w：%s 必须是普通文件", ErrUnsafeBuildSource, entry.Name())
		}
		dockerfileCount++
		if err := validateDockerfileBases(filepath.Join(deployPath, entry.Name())); err != nil {
			return err
		}
	}
	if dockerfileCount == 0 {
		return fmt.Errorf("%w：未找到 Dockerfile", ErrUnsafeBuildSource)
	}
	rules, err := loadDockerignore(filepath.Join(root, ".dockerignore"))
	if err != nil {
		return err
	}
	for _, candidate := range protectedBuildContextPaths {
		ignored, err := rules.ignored(candidate)
		if err != nil {
			return err
		}
		if !ignored {
			return fmt.Errorf("%w：构建上下文仍会包含 %s", ErrUnsafeBuildSource, candidate)
		}
	}
	return nil
}

func validateDockerfileBases(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w：打开 %s：%v", ErrUnsafeBuildSource, filepath.Base(path), err)
	}
	defer file.Close()
	stages := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	fromCount := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		fromCount++
		index := 1
		for index < len(fields) && strings.HasPrefix(fields[index], "--") {
			index++
		}
		if index >= len(fields) {
			return fmt.Errorf("%w：%s:%d 的 FROM 缺少基础镜像", ErrUnsafeBuildSource, filepath.Base(path), lineNumber)
		}
		base := fields[index]
		if _, localStage := stages[strings.ToLower(base)]; !localStage && !pinnedBasePattern.MatchString(base) {
			return fmt.Errorf("%w：%s:%d 的基础镜像未固定摘要", ErrUnsafeBuildSource, filepath.Base(path), lineNumber)
		}
		for aliasIndex := index + 1; aliasIndex+1 < len(fields); aliasIndex++ {
			if strings.EqualFold(fields[aliasIndex], "AS") {
				alias := strings.ToLower(fields[aliasIndex+1])
				if alias == "" {
					return fmt.Errorf("%w：%s:%d 的阶段别名无效", ErrUnsafeBuildSource, filepath.Base(path), lineNumber)
				}
				stages[alias] = struct{}{}
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w：读取 %s：%v", ErrUnsafeBuildSource, filepath.Base(path), err)
	}
	if fromCount == 0 {
		return fmt.Errorf("%w：%s 不包含 FROM 构建阶段", ErrUnsafeBuildSource, filepath.Base(path))
	}
	return nil
}

type dockerignoreRule struct {
	negated bool
	pattern string
	regex   *regexp.Regexp
}

type dockerignoreRules []dockerignoreRule

func loadDockerignore(path string) (dockerignoreRules, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w：.dockerignore 必须是普通文件", ErrUnsafeBuildSource)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w：打开 .dockerignore：%v", ErrUnsafeBuildSource, err)
	}
	defer file.Close()
	var rules dockerignoreRules
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := strings.HasPrefix(line, "!")
		if negated {
			line = strings.TrimPrefix(line, "!")
		}
		line = strings.Trim(line, "/")
		if line == "" || line == "." {
			continue
		}
		compiled, err := compileDockerignorePattern(line)
		if err != nil {
			return nil, fmt.Errorf("%w：无效 .dockerignore 规则 %q：%v", ErrUnsafeBuildSource, line, err)
		}
		rules = append(rules, dockerignoreRule{negated: negated, pattern: line, regex: compiled})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w：读取 .dockerignore：%v", ErrUnsafeBuildSource, err)
	}
	return rules, nil
}

func (rules dockerignoreRules) ignored(candidate string) (bool, error) {
	candidate = filepath.ToSlash(filepath.Clean(filepath.FromSlash(candidate)))
	if candidate == "." || strings.HasPrefix(candidate, "../") {
		return false, fmt.Errorf("%w：上下文探针路径无效", ErrUnsafeBuildSource)
	}
	ignored := false
	for _, rule := range rules {
		matched := false
		for current := candidate; current != "." && current != ""; current = filepath.ToSlash(filepath.Dir(filepath.FromSlash(current))) {
			if rule.regex.MatchString(current) {
				matched = true
				break
			}
		}
		if matched {
			ignored = !rule.negated
		}
	}
	return ignored, nil
}

func compileDockerignorePattern(pattern string) (*regexp.Regexp, error) {
	anchored := strings.Contains(pattern, "/")
	var expression strings.Builder
	if anchored {
		expression.WriteString("^")
	} else {
		expression.WriteString(`^(?:.*/)?`)
	}
	for index := 0; index < len(pattern); index++ {
		character := pattern[index]
		switch character {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				index++
				if index+1 < len(pattern) && pattern[index+1] == '/' {
					index++
					expression.WriteString(`(?:.*/)?`)
				} else {
					expression.WriteString(`.*`)
				}
			} else {
				expression.WriteString(`[^/]*`)
			}
		case '?':
			expression.WriteString(`[^/]`)
		default:
			expression.WriteString(regexp.QuoteMeta(string(character)))
		}
	}
	expression.WriteString("$")
	return regexp.Compile(expression.String())
}
