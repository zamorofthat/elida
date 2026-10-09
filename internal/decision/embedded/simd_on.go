//go:build amd64 && goexperiment.simd

package embedded

// SIMDEnabled reports whether this binary was built with the SIMD kernels
// GoMLX gates behind `amd64 && goexperiment.simd`. Those kernels are the
// difference between the measured 21 ms and 501 ms at 126 tokens, which is
// the difference between an inline capability and async-only.
func SIMDEnabled() bool { return true }
