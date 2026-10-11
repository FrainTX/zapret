//go:build linux && 386

package engine

import "embed"

//go:embed bundle/linux_386 bundle/fake bundle/MANIFEST.sha256 bundle/VERSION
var bundleFS embed.FS

const bundleDir = "bundle/linux_386"
