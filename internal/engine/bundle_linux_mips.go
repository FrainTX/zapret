//go:build linux && mips

package engine

import "embed"

//go:embed bundle/linux_mips bundle/fake bundle/MANIFEST.sha256 bundle/VERSION
var bundleFS embed.FS

const bundleDir = "bundle/linux_mips"
