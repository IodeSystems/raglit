//go:build release

package main

import _ "embed"

// releaseBuild marks a binary built by the release workflow (-tags release).
// doctor tells it from a `go install` build by this, since both lack a
// checkout and the update path differs: a download, not `go install`.
const releaseBuild = true

// releaseManifestURL is where a release build looks for the newest release's
// signed manifest; its signature is beside it, + ".minisig". Only release
// builds carry a download location (releaseupdate.go).
const releaseManifestURL = "https://github.com/IodeSystems/raglit/releases/latest/download/raglit-manifest.json"

// releaseKey is the minisign public key the manifest must be signed with:
// dun's release key, so one owner key signs both (Carl 2026-11-04).
//
//go:embed minisign.pub
var releaseKey string
