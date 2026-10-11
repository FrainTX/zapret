//go:build linux && arm64

package engine

import "embed"

//go:embed bundle/linux_arm64 bundle/fake bundle/MANIFEST.sha256 bundle/VERSION
var bundleFS embed.FS

const bundleDir = "bundle/linux_arm64"
