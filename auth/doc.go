// Package auth provides token-based authentication for the document store.
//
// It separates a transport-agnostic core from the HTTP layer: CreateSession,
// EndSession, and Authenticate contain the session logic and are usable from any
// caller, while Login, Logout, and HandlePreflight are thin adapters that speak
// HTTP. Session tokens are generated with crypto/rand and expire after a fixed
// TTL; preloaded tokens (from a tokens file) are also supported.
package auth
