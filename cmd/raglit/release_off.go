//go:build !release

package main

// releaseBuild is false for every build except the release workflow's
// (-tags release); see release_on.go. Such a build has no download location
// and no key, so it never updates itself from a release.
const releaseBuild = false

const releaseManifestURL = ""

var releaseKey string
