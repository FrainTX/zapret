//go:build !(linux && (amd64 || 386 || arm64 || arm || mips || mipsle)) && !(windows && (amd64 || 386))

package engine

import "embed"

// No engine build for this platform: decoys only, the system nfqws is used.
//
//go:embed bundle/fake bundle/MANIFEST.sha256 bundle/VERSION
var bundleFS embed.FS

const bundleDir = ""
