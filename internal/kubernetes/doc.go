// Package kubernetes is the innermost layer: everything that talks to
// Kubernetes clusters through client-go. It must not import internal/services
// or internal/tools — this layer works without an MCP server.
package kubernetes
