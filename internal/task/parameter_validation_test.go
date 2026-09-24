package task

import (
	"errors"
	"math"
	"strings"
	"testing"

	"yunling.local/platform/internal/script"
)

func TestValidateParameters(t *testing.T) {
	definitions := []script.ParameterDefinition{
		{Name: "客户", Type: "string", Required: true},
		{Name: "数量", Type: "number", Required: true},
		{Name: "启用", Type: "boolean", Required: true},
		{Name: "备注", Type: "string"},
	}
	valid := func() map[string]any { return map[string]any{"客户": "", "数量": float64(0), "启用": false} }
	if err := validateParameters(definitions, valid(), nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"客户", "数量", "启用"} {
		values := valid()
		delete(values, name)
		if err := validateParameters(definitions, values, nil); !errors.Is(err, ErrInvalidParameters) {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	for _, value := range []any{"SECRET-MUST-NOT-APPEAR", nil, math.Inf(1), []int{1}} {
		values := valid()
		values["数量"] = value
		err := validateParameters(definitions, values, nil)
		if !errors.Is(err, ErrInvalidParameters) || strings.Contains(err.Error(), "SECRET-MUST-NOT-APPEAR") {
			t.Fatalf("invalid parameter error: %v", err)
		}
	}
	if err := validateParameters(nil, map[string]any{"旧版参数": []int{1}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateParametersAcceptsSecretReferenceWithoutPlaintext(t *testing.T) {
	definitions := []script.ParameterDefinition{{Name: "访问令牌", Type: "string", Required: true}}
	refs := map[string]string{"访问令牌": "secret-id"}
	if err := validateParameters(definitions, nil, refs); err != nil {
		t.Fatal(err)
	}
	if err := validateParameters(definitions, map[string]any{"访问令牌": "hidden"}, refs); !errors.Is(err, ErrInvalidParameters) {
		t.Fatalf("ambiguous binding must fail: %v", err)
	}
	definitions[0].Type = "number"
	if err := validateParameters(definitions, nil, refs); !errors.Is(err, ErrInvalidParameters) {
		t.Fatalf("secrets are strings: %v", err)
	}
}
