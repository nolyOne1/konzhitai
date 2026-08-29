package secret

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"sort"
	"strings"
)

var mask = []byte("******")

type Redactor struct{}

func NewRedactor() *Redactor { return &Redactor{} }

func (r *Redactor) Mask(text []byte, values [][]byte) []byte {
	result := append([]byte(nil), text...)
	variants := make(map[string]struct{}, len(values)*7)
	for _, value := range values {
		if len(value) == 0 {
			continue
		}
		plain := string(value)
		variants[plain] = struct{}{}
		variants[base64.StdEncoding.EncodeToString(value)] = struct{}{}
		variants[base64.RawStdEncoding.EncodeToString(value)] = struct{}{}
		variants[base64.URLEncoding.EncodeToString(value)] = struct{}{}
		variants[base64.RawURLEncoding.EncodeToString(value)] = struct{}{}
		variants[url.QueryEscape(plain)] = struct{}{}
		variants[url.PathEscape(plain)] = struct{}{}
	}
	ordered := make([]string, 0, len(variants))
	for variant := range variants {
		if variant != "" && variant != string(mask) {
			ordered = append(ordered, variant)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, variant := range ordered {
		result = bytes.ReplaceAll(result, []byte(variant), mask)
		lowerEncoded := strings.ToLower(variant)
		if strings.Contains(variant, "%") && lowerEncoded != variant {
			result = bytes.ReplaceAll(result, []byte(lowerEncoded), mask)
		}
	}
	return result
}
