//go:build releaseintegration

package release

import (
	"errors"
	"regexp"
	"strconv"
)

var releaseIntegrationImagePattern = regexp.MustCompile(`^127\.0\.0\.1:([1-9][0-9]{0,4})/yunling-(services|web|ops)@sha256:[0-9a-f]{64}$`)

func NewReleaseIntegrationPolicy(repositoryID int64, owner string, images Images) (ManifestPolicy, error) {
	policy := ManifestPolicy{
		RepositoryID: repositoryID,
		Owner:        owner,
		origin:       originReleaseIntegration,
		allowedImages: map[string]string{
			"services": images.Services,
			"web":      images.Web,
			"ops":      images.Ops,
		},
	}
	if repositoryID <= 0 || !ownerPattern.MatchString(owner) ||
		!validReleaseIntegrationImage(images.Services, "services") ||
		!validReleaseIntegrationImage(images.Web, "web") ||
		!validReleaseIntegrationImage(images.Ops, "ops") {
		return ManifestPolicy{}, errors.New("发布演练镜像白名单无效")
	}
	return policy, nil
}

func validateReleaseIntegrationImages(images ServiceImages) bool {
	return images.API == images.Scheduler &&
		validReleaseIntegrationImage(images.API, "services") &&
		validReleaseIntegrationImage(images.Web, "web") &&
		validReleaseIntegrationImage(images.Ops, "ops")
}

func validReleaseIntegrationImage(image, service string) bool {
	match := releaseIntegrationImagePattern.FindStringSubmatch(image)
	if len(match) != 3 || match[2] != service {
		return false
	}
	port, err := strconv.Atoi(match[1])
	return err == nil && port <= 65535
}
