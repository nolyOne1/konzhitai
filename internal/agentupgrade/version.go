package agentupgrade

import "strings"

// compareStableVersions compares ASCII x.y.z versions with an optional lowercase
// v prefix. Unknown version formats are not safe to order for an upgrade.
func compareStableVersions(left, right string) (int, bool) {
	leftParts, leftOK := stableVersionParts(left)
	rightParts, rightOK := stableVersionParts(right)
	if !leftOK || !rightOK {
		return 0, false
	}
	for index := range leftParts {
		if len(leftParts[index]) < len(rightParts[index]) {
			return -1, true
		}
		if len(leftParts[index]) > len(rightParts[index]) {
			return 1, true
		}
		if compared := strings.Compare(leftParts[index], rightParts[index]); compared != 0 {
			return compared, true
		}
	}
	return 0, true
}

func stableVersionParts(version string) ([3]string, bool) {
	var parts [3]string
	if len(version) == 0 || len(version) > 64 {
		return parts, false
	}
	if version[0] == 'v' {
		version = version[1:]
	}
	segments := strings.Split(version, ".")
	if len(segments) != len(parts) {
		return parts, false
	}
	for index, segment := range segments {
		if len(segment) == 0 || (len(segment) > 1 && segment[0] == '0') {
			return parts, false
		}
		for digit := 0; digit < len(segment); digit++ {
			if segment[digit] < '0' || segment[digit] > '9' {
				return parts, false
			}
		}
		parts[index] = segment
	}
	return parts, true
}
