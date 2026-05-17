package instruction

import (
	"fmt"
	"regexp"
)

// Rule defines a single instruction-specific policy rule.
type Rule struct {
	Name     string   `yaml:"name" json:"name"`
	Patterns []string `yaml:"patterns" json:"patterns"`
	Severity string   `yaml:"severity" json:"severity"`
	Action   string   `yaml:"action" json:"action"` // "block" or "flag"
}

// compiledRule pairs a Rule with its compiled regex patterns.
type compiledRule struct {
	rule     Rule
	patterns []*regexp.Regexp
}

// Scanner evaluates instruction file content against compiled rules.
type Scanner struct {
	rules []compiledRule
}

// NewScanner compiles the rule patterns and returns a ready-to-use Scanner.
// Returns an error if any regex pattern is invalid.
func NewScanner(rules []Rule) (*Scanner, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		cr := compiledRule{rule: r}
		for _, pattern := range r.Patterns {
			re, err := regexp.Compile("(?i)" + pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid pattern in rule %q: %w", r.Name, err)
			}
			cr.patterns = append(cr.patterns, re)
		}
		compiled = append(compiled, cr)
	}
	return &Scanner{rules: compiled}, nil
}

// Scan checks content against all rules and returns the result.
func (s *Scanner) Scan(content string) ScanResult {
	var result ScanResult
	for _, cr := range s.rules {
		for _, re := range cr.patterns {
			if match := re.FindString(content); match != "" {
				result.Violations = append(result.Violations, Violation{
					RuleName:    cr.rule.Name,
					Severity:    cr.rule.Severity,
					Action:      cr.rule.Action,
					MatchedText: match,
				})
				if cr.rule.Action == "block" {
					result.ShouldBlock = true
				}
				break // One match per rule is enough
			}
		}
	}
	return result
}
