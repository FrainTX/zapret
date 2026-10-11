//go:build windows && 386

package engine

import "embed"

//go:embed bundle/windows_386 bundle/fake bundle/MANIFEST.sha256 bundle/VERSION
var bundleFS embed.FS

const bundleDir = "bundle/windows_386"
