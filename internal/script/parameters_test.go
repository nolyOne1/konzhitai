package script

import (
	"context"
	"errors"
	"testing"
)

func TestScriptRejectsInvalidParameterDefinitionsBeforeSaving(t *testing.T) {
	for name, definitions := range map[string][]ParameterDefinition{
		"empty name":   {{Name: " ", Type: "string"}},
		"duplicate":    {{Name: "日期", Type: "string"}, {Name: " 日期 ", Type: "string"}},
		"unknown type": {{Name: "日期", Type: "object"}},
	} {
		t.Run(name, func(t *testing.T) {
			service := NewService(nil, nil, nil)
			_, err := service.SaveDraft(context.Background(), DraftInput{ParameterDefinitions: definitions})
			if !errors.Is(err, ErrInvalidParameters) {
				t.Fatalf("invalid draft parameters accepted: %v", err)
			}
			_, err = service.Publish(context.Background(), PublishInput{
				ScriptID: "script", Content: []byte("echo ready"), Runtime: "bash", Entrypoint: "main.sh",
				ReleaseNotes: "验证参数", Distribution: DistributionRule{Mode: DistributionOnDemand}, ParameterDefinitions: definitions,
			})
			if !errors.Is(err, ErrInvalidParameters) {
				t.Fatalf("invalid published parameters accepted: %v", err)
			}
		})
	}
}

func TestScriptParametersRetainRequiredAndDescription(t *testing.T) {
	parameters, err := normalizeParameters([]ParameterDefinition{{Name: " 日期 ", Type: "string", Required: true, Description: " 归档日期 "}})
	if err != nil || len(parameters) != 1 || parameters[0].Name != "日期" || !parameters[0].Required || parameters[0].Description != "归档日期" {
		t.Fatalf("normalized definition=%+v error=%v", parameters, err)
	}
}
