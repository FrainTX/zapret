//go:build linux && mipsle

package engine

import "embed"

//go:embed bundle/linux_mipsle bundle/fake bundle/MANIFEST.sha256 bundle/VERSION
var bundleFS embed.FS

const bundleDir = "bundle/linux_mipsle"
