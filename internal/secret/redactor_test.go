package secret_test

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"testing"

	"yunling.local/platform/internal/secret"
)

func TestRedactorMasksExactBase64AndURLEncodedValues(t *testing.T) {
	value := []byte("very-secret/+ value")
	text := []byte("plain=very-secret/+ value base64=" + base64.StdEncoding.EncodeToString(value) + " url=" + url.QueryEscape(string(value)))

	got := secret.NewRedactor().Mask(text, [][]byte{value})
	if bytes.Contains(got, value) || bytes.Contains(got, []byte(base64.StdEncoding.EncodeToString(value))) || bytes.Contains(got, []byte(url.QueryEscape(string(value)))) {
		t.Fatalf("日志仍包含敏感值：%s", got)
	}
	if bytes.Count(got, []byte("******")) != 3 {
		t.Fatalf("三种编码形态都必须替换：%s", got)
	}
}

func TestRedactorMasksLongerOverlappingSecretFirst(t *testing.T) {
	got := secret.NewRedactor().Mask([]byte("token=abc123"), [][]byte{[]byte("abc"), []byte("abc123")})
	if string(got) != "token=******" {
		t.Fatalf("重叠敏感值必须优先完整遮盖：%s", got)
	}
}

func TestRedactorDoesNotMutateInput(t *testing.T) {
	input := []byte("pwd=secret")
	copyBefore := append([]byte(nil), input...)
	_ = secret.NewRedactor().Mask(input, [][]byte{[]byte("secret")})
	if !bytes.Equal(input, copyBefore) {
		t.Fatal("日志脱敏不得修改调用方缓冲")
	}
}
