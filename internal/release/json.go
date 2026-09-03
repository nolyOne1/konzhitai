package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxManifestBytes = 1 << 20

func decodeStrictJSON(reader io.Reader, value any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
	if err != nil {
		return fmt.Errorf("读取 JSON：%w", err)
	}
	if len(data) > maxManifestBytes {
		return errors.New("JSON 超过 1 MiB 限制")
	}

	duplicateDecoder := json.NewDecoder(bytes.NewReader(data))
	if err := rejectDuplicateKeys(duplicateDecoder); err != nil {
		return err
	}
	if token, err := duplicateDecoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON 包含尾随值：%v", token)
		}
		return fmt.Errorf("检查 JSON 结尾：%w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("解析 JSON：%w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("JSON 包含尾随值")
		}
		return fmt.Errorf("检查 JSON 结尾：%w", err)
	}
	return nil
}

func rejectDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("读取 JSON 标记：%w", err)
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
				return fmt.Errorf("读取 JSON 对象键：%w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON 对象键必须是字符串")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("JSON 键重复：%s", key)
			}
			seen[key] = struct{}{}
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("读取 JSON 对象结尾：%w", err)
		}
		if end != json.Delim('}') {
			return errors.New("JSON 对象结尾无效")
		}
	case '[':
		for decoder.More() {
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("读取 JSON 数组结尾：%w", err)
		}
		if end != json.Delim(']') {
			return errors.New("JSON 数组结尾无效")
		}
	default:
		return fmt.Errorf("JSON 起始分隔符无效：%v", delimiter)
	}
	return nil
}
