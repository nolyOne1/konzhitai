package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var ErrUntrustedCandidateRun = errors.New("候选来源运行不受信任")

type RunMetadata struct {
	Workflow     string
	Conclusion   string
	Branch       string
	Event        string
	RepositoryID int64
}

type CandidatePolicy struct {
	RepositoryID int64
}

func ValidateCandidateRun(run RunMetadata, policy CandidatePolicy) error {
	if policy.RepositoryID <= 0 || run.Workflow != "云令 CI" || run.Conclusion != "success" ||
		run.Branch != "main" || run.Event != "push" || run.RepositoryID != policy.RepositoryID {
		return ErrUntrustedCandidateRun
	}
	return nil
}

func DecodeRunMetadata(reader io.Reader) (RunMetadata, error) {
	if reader == nil {
		return RunMetadata{}, errors.New("GitHub 运行元数据为空")
	}
	body, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return RunMetadata{}, errors.New("GitHub 运行元数据超过 1 MiB")
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return RunMetadata{}, fmt.Errorf("解析 GitHub 运行元数据：%w", err)
	}
	var wire struct {
		Workflow   string `json:"name"`
		Conclusion string `json:"conclusion"`
		Branch     string `json:"head_branch"`
		Event      string `json:"event"`
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return RunMetadata{}, fmt.Errorf("解析 GitHub 运行元数据：%w", err)
	}
	return RunMetadata{
		Workflow: wire.Workflow, Conclusion: wire.Conclusion, Branch: wire.Branch,
		Event: wire.Event, RepositoryID: wire.Repository.ID,
	}, nil
}

func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON 对象键无效")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("JSON 对象键重复：%s", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("JSON 对象未正确结束")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("JSON 数组未正确结束")
			}
		default:
			return errors.New("JSON 分隔符无效")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON 包含尾随内容")
		}
		return err
	}
	return nil
}
