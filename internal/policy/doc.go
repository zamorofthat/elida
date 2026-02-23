// Package policy implements the policy engine for evaluating sessions
// against configurable rules. It supports metric-based rules (bytes, requests,
// tokens, duration) and content-matching rules (regex patterns).
//
// # Concurrency
//
// The Engine is safe for concurrent use. All methods that access shared state
// are protected by sync.RWMutex.
//
// IMPORTANT: When reading shared slices (rules, compiledRules) that can be
// modified by ReloadConfig, always use the "snapshot under lock" pattern:
//
//	// CORRECT - snapshot under lock, then iterate
//	e.mu.RLock()
//	rules := e.compiledRules
//	e.mu.RUnlock()
//	for _, r := range rules { ... }
//
//	// WRONG - reading without lock causes data race
//	for _, r := range e.compiledRules { ... }
//
// The snapshot works because:
//   - Assigning a slice copies the header (ptr, len, cap), not the data
//   - ReloadConfig creates a new backing array via make()
//   - The old array remains valid for goroutines holding a reference
//
// Always run tests with -race flag: go test -race ./...
package policy
