// Package services sits between the MCP tools and the kubernetes layer:
// it orchestrates calls, applies limits, and shapes responses. It may
// import internal/kubernetes; it must not import internal/tools.
package services
