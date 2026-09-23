package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"yunling.local/platform/internal/script"
)

var ErrInvalidParameters = errors.New("任务参数不符合脚本版本要求")

func validateVersionParameters(ctx context.Context, tx pgx.Tx, scriptID, versionID string, values map[string]any, refsJSON []byte) error {
	var manifestJSON []byte
	if err := tx.QueryRow(ctx, `SELECT manifest FROM script_versions WHERE id=$1 AND script_id=$2`, versionID, scriptID).Scan(&manifestJSON); errors.Is(err, pgx.ErrNoRows) {
		return ErrVersionUnavailable
	} else if err != nil {
		return fmt.Errorf("读取脚本参数定义：%w", err)
	}
	var manifest script.Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return fmt.Errorf("解析脚本参数定义：%w", err)
	}
	refs := map[string]string{}
	if err := json.Unmarshal(refsJSON, &refs); err != nil {
		return fmt.Errorf("解析敏感参数引用：%w", err)
	}
	return validateParameters(manifest.ParameterDefinitions, values, refs)
}

// Validate the resolved immutable version after applying manual overrides. Secret
// references travel in YUNLING_SECRETS_JSON and are never decrypted here.
func validateParameters(definitions []script.ParameterDefinition, values map[string]any, refs map[string]string) error {
	for _, definition := range definitions {
		if strings.TrimSpace(refs[definition.Name]) != "" {
			if definition.Type != "string" {
				return fmt.Errorf("%w：%s 的敏感参数必须为文本类型", ErrInvalidParameters, definition.Name)
			}
			if _, exists := values[definition.Name]; exists {
				return fmt.Errorf("%w：%s 不能同时配置普通参数和敏感参数", ErrInvalidParameters, definition.Name)
			}
			continue
		}
		value, exists := values[definition.Name]
		if !exists || value == nil {
			if definition.Required {
				return fmt.Errorf("%w：缺少必填参数 %s", ErrInvalidParameters, definition.Name)
			}
			continue
		}
		valid := false
		switch definition.Type {
		case "string":
			_, valid = value.(string)
		case "boolean":
			_, valid = value.(bool)
		case "number":
			switch number := value.(type) {
			case float64:
				valid = !math.IsNaN(number) && !math.IsInf(number, 0)
			case float32:
				valid = !math.IsNaN(float64(number)) && !math.IsInf(float64(number), 0)
			case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
				valid = true
			case json.Number:
				parsed, err := number.Float64()
				valid = err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
			}
		}
		if !valid {
			return fmt.Errorf("%w：%s 应为 %s 类型", ErrInvalidParameters, definition.Name, definition.Type)
		}
	}
	return nil
}
