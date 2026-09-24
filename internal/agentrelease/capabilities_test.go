package agentrelease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func declaredImport(t *testing.T) ImportInput {
	t.Helper()
	input := validImportInput("0.2.6")
	input.Capabilities = []string{SelfUpgradeCapability, RunArtifactsCapability}
	manifest := storedManifest{Version: input.Version, Capabilities: input.Capabilities}
	for _, item := range input.Artifacts {
		manifest.Artifacts = append(manifest.Artifacts, storedArtifact{OS: item.OS, Arch: item.Arch, FileName: item.FileName, ByteSize: item.ByteSize, SHA256: item.SHA256})
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	input.ManifestJSON = body
	digest := sha256.Sum256(body)
	input.ManifestSHA256 = hex.EncodeToString(digest[:])
	return input
}

func TestImportDeclaredCapabilitiesRemainBoundToManifest(t *testing.T) {
	input := declaredImport(t)
	service := NewService(newMemoryRepository(), newMemoryObjectStore(), time.Now)
	input.Recommend = true
	release, err := service.Import(context.Background(), input)
	if err != nil || !reflect.DeepEqual(release.Capabilities, input.Capabilities) || release.ManifestSHA256 != input.ManifestSHA256 {
		t.Fatalf("declared capabilities lost: release=%+v error=%v", release, err)
	}
	public, err := service.releaseManifest(context.Background())
	if err != nil || !reflect.DeepEqual(public.Capabilities, input.Capabilities) {
		t.Fatalf("public capabilities=%v error=%v", public.Capabilities, err)
	}
	for _, mutate := range []func(*ImportInput){
		func(input *ImportInput) { input.Capabilities = []string{SelfUpgradeCapability} },
		func(input *ImportInput) { input.Artifacts[0].ByteSize++ },
		func(input *ImportInput) { input.ManifestJSON = append(input.ManifestJSON, ' ') },
		func(input *ImportInput) { input.ManifestJSON = nil },
		func(input *ImportInput) { input.Capabilities = []string{"unimplemented_capability"} },
		func(input *ImportInput) {
			input.Capabilities = []string{RunArtifactsCapability, RunArtifactsCapability}
		},
	} {
		candidate := declaredImport(t)
		mutate(&candidate)
		objects := newMemoryObjectStore()
		_, err := NewService(newMemoryRepository(), objects, time.Now).Import(context.Background(), candidate)
		if err == nil || objects.putCalls != 0 {
			t.Fatalf("unbound metadata accepted or written: %v writes=%d", err, objects.putCalls)
		}
	}
}

func TestDeclaredCatalogCapabilitiesSurviveBootstrapAndAreNotInferred(t *testing.T) {
	input := declaredImport(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), input.ManifestJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, item := range input.Artifacts {
		body, err := io.ReadAll(item.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, item.FileName), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := catalog.Manifest()
	manifest.Capabilities[0] = "changed"
	if !reflect.DeepEqual(catalog.Manifest().Capabilities, input.Capabilities) {
		t.Fatal("catalog leaked capability slice")
	}
	service := NewService(newMemoryRepository(), newMemoryObjectStore(), time.Now)
	if err := service.BootstrapFromDirectory(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	release, err := service.Recommended(context.Background())
	if err != nil || !reflect.DeepEqual(release.Capabilities, input.Capabilities) {
		t.Fatalf("bootstrap lost declaration: %+v %v", release, err)
	}
	// Even a later version cannot acquire the new capability without declaring it.
	legacy, err := NewService(newMemoryRepository(), newMemoryObjectStore(), time.Now).Import(context.Background(), validImportInput("99.0.0"))
	if err != nil || !reflect.DeepEqual(legacy.Capabilities, []string{SelfUpgradeCapability}) {
		t.Fatalf("capability inferred from version: %+v %v", legacy, err)
	}
	broken := declaredImport(t)
	broken.Artifacts[0].SHA256 = "not-a-digest"
	if _, err := NewService(newMemoryRepository(), newMemoryObjectStore(), time.Now).Import(context.Background(), broken); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("package checks bypassed: %v", err)
	}
}
