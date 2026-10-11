//go:build windows && amd64

package engine

import "embed"

//go:embed bundle/windows_amd64 bundle/fake bundle/MANIFEST.sha256 bundle/VERSION
var bundleFS embed.FS

const bundleDir = "bundle/windows_amd64"
