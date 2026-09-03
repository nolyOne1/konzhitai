//go:build !releaseintegration

package release

func validateReleaseIntegrationImages(ServiceImages) bool {
	return false
}
