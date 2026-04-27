//go:build tools
// +build tools

// Package tools pins the versions of code-generation tools used by `make codegen`.
// It is never compiled into the binary (build tag `tools` excludes it from normal
// builds). go.mod is the single source of truth for the pinned tool versions.
package tools

import (
	_ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
)
