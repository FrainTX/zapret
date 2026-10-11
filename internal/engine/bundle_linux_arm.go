//go:build linux && arm

package engine

import "embed"

//go:embed bundle/linux_arm bundle/fake bundle/MANIFEST.sha256 bundle/VERSION
var bundleFS embed.FS

const bundleDir = "bundle/linux_arm"
