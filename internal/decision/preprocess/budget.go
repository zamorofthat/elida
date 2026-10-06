package preprocess

import "elida/internal/config"

// BudgetFrom builds a Budget from the operator-facing preprocessing
// configuration. It is the single place the two shapes meet, so a field
// added to one and forgotten on the other is a compile error or a test
// failure rather than a silently unenforced bound.
//
// The import direction is deliberate: preprocess imports config, never the
// reverse. internal/config has no dependency on this package, so there is no
// cycle, and the scheduler does not have to translate operator bounds itself.
//
// No clamping happens here. Config validation rejects out-of-range values at
// startup, and Run fails safe on a zero or negative bound (see Budget), so a
// Budget built from a misconfigured file analyzes nothing and reports the
// unset fields as coverage gaps rather than inventing a limit.
func BudgetFrom(c config.DecisionPreprocessingConfig) Budget {
	return Budget{
		MaxInputBytes:      c.MaxInputBytes,
		MaxAnalysisBytes:   c.MaxAnalysisBytes,
		MaxRepresentations: c.MaxRepresentations,
		MaxDecodeDepth:     c.MaxDecodeDepth,
		MaxExpansionRatio:  c.MaxExpansionRatio,
	}
}
