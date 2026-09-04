package release

import (
	"errors"
	"testing"
)

func TestRenderComposeOverrideContainsOnlyFourImageFields(t *testing.T) {
	release := testGHCRRelease(t, 101)
	content, err := RenderComposeOverride(release)
	if err != nil {
		t.Fatal(err)
	}
	want := "services:\n" +
		"  api:\n    image: " + release.Images.API + "\n" +
		"  scheduler:\n    image: " + release.Images.Scheduler + "\n" +
		"  web:\n    image: " + release.Images.Web + "\n" +
		"  ops:\n    image: " + release.Images.Ops + "\n"
	if string(content) != want {
		t.Fatalf("覆盖文件不匹配：\ngot:\n%swant:\n%s", content, want)
	}
}

func TestRenderComposeOverrideRejectsUnvalidatedRelease(t *testing.T) {
	release := StoredRelease{
		TargetID: "101",
		Origin:   OriginGHCR,
		Images: ServiceImages{
			API:       "alpine:latest",
			Scheduler: "alpine:latest",
			Web:       "alpine:latest",
			Ops:       "alpine:latest",
		},
	}
	if _, err := RenderComposeOverride(release); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("未验证镜像不得进入 Compose：%v", err)
	}
}
