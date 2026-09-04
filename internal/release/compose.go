package release

import (
	"bytes"
	"fmt"
)

func RenderComposeOverride(release StoredRelease) ([]byte, error) {
	if err := validateStoredRelease(release, false); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.WriteString("services:\n")
	for _, service := range []struct {
		name  string
		image string
	}{
		{name: "api", image: release.Images.API},
		{name: "scheduler", image: release.Images.Scheduler},
		{name: "web", image: release.Images.Web},
		{name: "ops", image: release.Images.Ops},
	} {
		if _, err := fmt.Fprintf(&output, "  %s:\n    image: %s\n", service.name, service.image); err != nil {
			return nil, fmt.Errorf("生成 Compose 覆盖：%w", err)
		}
	}
	return output.Bytes(), nil
}
