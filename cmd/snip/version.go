package main

// buildVersion is the SINGLE source of truth for the version. Release builds
// stamp it via -X (go build -ldflags "-X main.buildVersion=vX.Y.Z"); the
// declaration's SHAPE is pinned by TestVersionDeclarationShape, which mirrors
// the release tooling's parse: exactly one `var buildVersion = "…"` line.
// Do not reformat this line.
var buildVersion = "0.1.1"
