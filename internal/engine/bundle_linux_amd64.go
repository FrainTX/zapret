//go:build linux && amd64

package engine

import "embed"

//go:embed bundle/linux_amd64 bundle/fake bundle/MANIFEST.sha256 bundle/VERSION
var bundleFS embed.FS

const bundleDir = "bundle/linux_amd64"
