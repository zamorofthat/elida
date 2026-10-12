//go:build !(amd64 && goexperiment.simd)

package embedded

// SIMDEnabled reports whether this binary was built with GoMLX's SIMD
// kernels. See simd_on.go.
func SIMDEnabled() bool { return false }
