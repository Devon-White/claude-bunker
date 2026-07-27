package main

// This file centralizes repository-wide `go generate` directives.
//
// genfeatures derives the firewall OCI Dev Container Feature's packaged
// scripts and builtin-domains.txt from the canonical sources in
// internal/container, so the VS Code/Codespaces path can never silently
// drift from bunker's native firewall. See features/firewall_drift_test.go.
//go:generate go run ./cmd/genfeatures
