// Package chaos provides benchmarking tests for the policy engine.
// These tests use known-bad prompts to measure false positive/negative rates.
package chaos

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"elida/internal/policy"
)

// Scenario represents a test scenario from scenarios.yaml
type Scenario struct {
	Name             string `yaml:"name"`
	Category         string `yaml:"category"`
	Target           string `yaml:"target"` // "request" or "response"
	Input            string `yaml:"input"`
	ExpectedAction   string `yaml:"expected_action"`   // "pass", "flag", "block", "terminate"
	ExpectedSeverity string `yaml:"expected_severity"` // "info", "warning", "critical", or null
	Description      string `yaml:"description"`
}

// ScenariosFile represents the structure of scenarios.yaml
type ScenariosFile struct {
	Version     string     `yaml:"version"`
	Description string     `yaml:"description"`
	Scenarios   []Scenario `yaml:"scenarios"`
}

// loadScenarios loads scenarios from the YAML file
func loadScenarios(t *testing.T) []Scenario {
	t.Helper()

	// Find scenarios.yaml relative to test file
	scenariosPath := filepath.Join("scenarios.yaml")
	if _, err := os.Stat(scenariosPath); os.IsNotExist(err) {
		// Try from project root
		scenariosPath = filepath.Join("test", "chaos", "scenarios.yaml")
	}

	data, err := os.ReadFile(scenariosPath)
	if err != nil {
		t.Fatalf("failed to read scenarios.yaml: %v", err)
	}

	var file ScenariosFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatalf("failed to parse scenarios.yaml: %v", err)
	}

	return file.Scenarios
}

// createPolicyEngine creates a policy engine with the standard preset
func createPolicyEngine() *policy.Engine {
	return policy.NewEngine(policy.Config{
		Enabled:        true,
		Mode:           "enforce",
		CaptureContent: true,
		MaxCaptureSize: 10000,
		Rules:          getStandardPresetRules(),
	})
}

// createStrictPolicyEngine creates a policy engine with the strict preset
func createStrictPolicyEngine() *policy.Engine {
	return policy.NewEngine(policy.Config{
		Enabled:        true,
		Mode:           "enforce",
		CaptureContent: true,
		MaxCaptureSize: 10000,
		Rules:          getStrictPresetRules(),
	})
}

// ChaosResults tracks test results for reporting
type ChaosResults struct {
	Total            int
	Passed           int
	Failed           int
	TruePositives    int // Correctly detected attacks
	TrueNegatives    int // Correctly passed benign
	FalsePositives   int // Benign flagged as attack
	FalseNegatives   int // Attack not detected
	FailedByCategory map[string]int
	DetailedFailures []string
}

// TestChaos_StandardPreset runs all scenarios against the standard policy preset
func TestChaos_StandardPreset(t *testing.T) {
	scenarios := loadScenarios(t)
	engine := createPolicyEngine()

	results := runScenarios(t, engine, scenarios)
	reportResults(t, "Standard Preset", results)
}

// TestChaos_StrictPreset runs all scenarios against the strict policy preset
func TestChaos_StrictPreset(t *testing.T) {
	scenarios := loadScenarios(t)
	engine := createStrictPolicyEngine()

	results := runScenarios(t, engine, scenarios)
	reportResults(t, "Strict Preset", results)
}

// runScenarios executes all scenarios and collects results
func runScenarios(t *testing.T, engine *policy.Engine, scenarios []Scenario) *ChaosResults {
	t.Helper()

	results := &ChaosResults{
		FailedByCategory: make(map[string]int),
	}

	for _, scenario := range scenarios {
		results.Total++

		var result *policy.ContentCheckResult
		sessionID := "chaos-test-" + scenario.Name

		// Evaluate based on target
		switch scenario.Target {
		case "response":
			result = engine.EvaluateResponseContent(sessionID, scenario.Input)
		default: // "request" or empty
			result = engine.EvaluateRequestContent(sessionID, scenario.Input)
		}

		// Determine actual action
		actualAction := getActualAction(result)

		// Check if result matches expectation
		if matchesExpectation(scenario, result) {
			results.Passed++

			// Categorize as true positive or true negative
			if scenario.ExpectedAction == "pass" {
				results.TrueNegatives++
			} else {
				results.TruePositives++
			}
		} else {
			results.Failed++
			results.FailedByCategory[scenario.Category]++

			// Categorize failure type
			if scenario.ExpectedAction == "pass" {
				results.FalsePositives++
				results.DetailedFailures = append(results.DetailedFailures,
					scenario.Name+" (FALSE POSITIVE): expected pass, got "+actualAction)
			} else {
				results.FalseNegatives++
				results.DetailedFailures = append(results.DetailedFailures,
					scenario.Name+" (FALSE NEGATIVE): expected "+scenario.ExpectedAction+", got "+actualAction)
			}

			// Log failure details in test output
			t.Logf("FAIL [%s] %s: expected %s, got %s",
				scenario.Category, scenario.Name, scenario.ExpectedAction, actualAction)
		}
	}

	return results
}

// getActualAction determines the action from a content check result
func getActualAction(result *policy.ContentCheckResult) string {
	if result == nil {
		return "pass"
	}
	if result.ShouldTerminate {
		return "terminate"
	}
	if result.ShouldBlock {
		return "block"
	}
	if len(result.Violations) > 0 {
		return "flag"
	}
	return "pass"
}

// matchesExpectation checks if the result matches the expected outcome
func matchesExpectation(scenario Scenario, result *policy.ContentCheckResult) bool {
	switch scenario.ExpectedAction {
	case "pass":
		// Should not have any violations
		return result == nil || len(result.Violations) == 0
	case "flag":
		// Should have violations but not block/terminate
		return result != nil && len(result.Violations) > 0 && !result.ShouldBlock && !result.ShouldTerminate
	case "block":
		// Should block but not terminate
		return result != nil && result.ShouldBlock && !result.ShouldTerminate
	case "terminate":
		// Should terminate
		return result != nil && result.ShouldTerminate
	default:
		return false
	}
}

// reportResults outputs the test results summary
func reportResults(t *testing.T, presetName string, results *ChaosResults) {
	t.Helper()

	t.Logf("\n=== Chaos Suite Results: %s ===", presetName)
	t.Logf("Total Scenarios: %d", results.Total)
	t.Logf("Passed: %d (%.1f%%)", results.Passed, float64(results.Passed)*100/float64(results.Total))
	t.Logf("Failed: %d (%.1f%%)", results.Failed, float64(results.Failed)*100/float64(results.Total))
	t.Logf("")
	t.Logf("True Positives:  %d (correctly detected attacks)", results.TruePositives)
	t.Logf("True Negatives:  %d (correctly passed benign)", results.TrueNegatives)
	t.Logf("False Positives: %d (benign flagged as attack)", results.FalsePositives)
	t.Logf("False Negatives: %d (attack not detected)", results.FalseNegatives)

	if len(results.FailedByCategory) > 0 {
		t.Logf("")
		t.Logf("Failures by Category:")
		for cat, count := range results.FailedByCategory {
			t.Logf("  %s: %d", cat, count)
		}
	}

	// Calculate rates
	if results.TruePositives+results.FalseNegatives > 0 {
		sensitivity := float64(results.TruePositives) / float64(results.TruePositives+results.FalseNegatives) * 100
		t.Logf("")
		t.Logf("Sensitivity (True Positive Rate): %.1f%%", sensitivity)
	}
	if results.TrueNegatives+results.FalsePositives > 0 {
		specificity := float64(results.TrueNegatives) / float64(results.TrueNegatives+results.FalsePositives) * 100
		t.Logf("Specificity (True Negative Rate): %.1f%%", specificity)
	}

	// Show detailed failures
	if len(results.DetailedFailures) > 0 {
		t.Logf("")
		t.Logf("Detailed Failures:")
		for _, failure := range results.DetailedFailures {
			t.Logf("  - %s", failure)
		}
	}
}

// TestChaos_PromptInjection focuses on prompt injection scenarios
func TestChaos_PromptInjection(t *testing.T) {
	scenarios := loadScenarios(t)
	engine := createPolicyEngine()

	var injectionScenarios []Scenario
	for _, s := range scenarios {
		if s.Category == "prompt_injection" {
			injectionScenarios = append(injectionScenarios, s)
		}
	}

	for _, scenario := range injectionScenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			result := engine.EvaluateRequestContent("test-"+scenario.Name, scenario.Input)
			actualAction := getActualAction(result)

			if !matchesExpectation(scenario, result) {
				t.Errorf("expected %s, got %s\nInput: %s",
					scenario.ExpectedAction, actualAction, truncate(scenario.Input, 100))
			}
		})
	}
}

// TestChaos_Jailbreak focuses on jailbreak scenarios
func TestChaos_Jailbreak(t *testing.T) {
	scenarios := loadScenarios(t)
	engine := createPolicyEngine()

	var jailbreakScenarios []Scenario
	for _, s := range scenarios {
		if s.Category == "jailbreak" {
			jailbreakScenarios = append(jailbreakScenarios, s)
		}
	}

	for _, scenario := range jailbreakScenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			result := engine.EvaluateRequestContent("test-"+scenario.Name, scenario.Input)
			actualAction := getActualAction(result)

			if !matchesExpectation(scenario, result) {
				t.Errorf("expected %s, got %s\nInput: %s",
					scenario.ExpectedAction, actualAction, truncate(scenario.Input, 100))
			}
		})
	}
}

// TestChaos_ToolAbuse focuses on tool abuse scenarios
func TestChaos_ToolAbuse(t *testing.T) {
	scenarios := loadScenarios(t)
	engine := createPolicyEngine()

	var toolScenarios []Scenario
	for _, s := range scenarios {
		if s.Category == "tool_abuse" {
			toolScenarios = append(toolScenarios, s)
		}
	}

	for _, scenario := range toolScenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var result *policy.ContentCheckResult
			if scenario.Target == "response" {
				result = engine.EvaluateResponseContent("test-"+scenario.Name, scenario.Input)
			} else {
				result = engine.EvaluateRequestContent("test-"+scenario.Name, scenario.Input)
			}
			actualAction := getActualAction(result)

			if !matchesExpectation(scenario, result) {
				t.Errorf("expected %s, got %s\nInput: %s",
					scenario.ExpectedAction, actualAction, truncate(scenario.Input, 100))
			}
		})
	}
}

// TestChaos_Benign tests that benign inputs don't trigger false positives
func TestChaos_Benign(t *testing.T) {
	scenarios := loadScenarios(t)
	engine := createPolicyEngine()

	var benignScenarios []Scenario
	for _, s := range scenarios {
		if s.Category == "benign" {
			benignScenarios = append(benignScenarios, s)
		}
	}

	for _, scenario := range benignScenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var result *policy.ContentCheckResult
			if scenario.Target == "response" {
				result = engine.EvaluateResponseContent("test-"+scenario.Name, scenario.Input)
			} else {
				result = engine.EvaluateRequestContent("test-"+scenario.Name, scenario.Input)
			}

			if result != nil && len(result.Violations) > 0 {
				t.Errorf("FALSE POSITIVE: benign input triggered %d violations",
					len(result.Violations))
				for _, v := range result.Violations {
					t.Logf("  - Rule: %s, Severity: %s", v.RuleName, v.Severity)
				}
			}
		})
	}
}

// truncate shortens a string for logging
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ============================================================
// Policy Preset Rules (copied from config.go for test isolation)
// ============================================================

func getStandardPresetRules() []policy.Rule {
	return []policy.Rule{
		// Rate limiting
		{Name: "rate_limit_high", Type: "requests_per_minute", Threshold: 60, Severity: "critical", Action: "block", Description: "Request rate exceeds 60/min"},

		// OWASP LLM01 - Prompt Injection
		{Name: "prompt_injection_ignore", Type: "content_match", Target: "request", Patterns: []string{
			"ignore\\s+(all\\s+)?(previous|prior|above|your)\\s+(instructions|prompts|rules)",
			"disregard\\s+(all\\s+)?(your\\s+)?(previous|prior|system)\\s+(instructions|prompts)",
			"forget\\s+(all\\s+)?(previous|prior|your)\\s+(instructions|training|rules)",
		}, Severity: "critical", Action: "block", Description: "LLM01: Prompt injection - instruction override"},
		{Name: "prompt_injection_jailbreak", Type: "content_match", Target: "request", Patterns: []string{
			"you\\s+are\\s+now\\s+(DAN|a\\s+new|an?\\s+unrestricted)",
			"enable\\s+(DAN|developer|jailbreak)\\s+mode",
			"jailbreak(ed)?\\s+(mode|prompt|enabled)",
		}, Severity: "critical", Action: "terminate", Description: "LLM01: Prompt injection - jailbreak attempt"},
		{Name: "prompt_injection_roleplay", Type: "content_match", Target: "request", Patterns: []string{
			"you\\s+are\\s+(now\\s+)?a\\s+.{0,30}(without|no)\\s+(any\\s+)?restrictions",
			"(pretend|act|behave)\\s+(like\\s+)?you\\s+(have|are)\\s+no\\s+(rules|restrictions|limits)",
			"(without|bypass|ignore)\\s+(any\\s+)?(safety|ethical)\\s+(guidelines|restrictions|rules)",
		}, Severity: "critical", Action: "block", Description: "LLM01: Prompt injection - roleplay restriction bypass"},

		// OWASP LLM02 - Insecure Output Handling
		{Name: "output_script_injection", Type: "content_match", Target: "response", Patterns: []string{
			"<script[^>]*>",
			"javascript:",
			"on(click|load|error|mouseover)\\s*=",
		}, Severity: "warning", Action: "flag", Description: "LLM02: Response contains XSS patterns"},
		{Name: "output_dangerous_code", Type: "content_match", Target: "response", Patterns: []string{
			"pickle\\.loads",
			"yaml\\.unsafe_load",
			"eval\\s*\\(.*input",
			"__import__\\s*\\(",
		}, Severity: "critical", Action: "flag", Description: "LLM02: Response contains unsafe code patterns"},

		// OWASP LLM07 - Tool abuse
		{Name: "tool_code_execution", Type: "content_match", Target: "request", Patterns: []string{
			"\"function\"\\s*:\\s*\"(run|execute|eval)_code\"",
			"\"name\"\\s*:\\s*\"(code_interpreter|execute_python|run_script)\"",
			"\"type\"\\s*:\\s*\"code_interpreter\"",
		}, Severity: "critical", Action: "flag", Description: "LLM07: Tool requests code execution"},
		{Name: "tool_credential_access", Type: "content_match", Target: "request", Patterns: []string{
			"\"function\"\\s*:\\s*\"(get|read|fetch)_(secret|credential|password|key)\"",
			"\"name\"\\s*:\\s*\"(vault_read|secret_manager|get_api_key)\"",
		}, Severity: "critical", Action: "block", Description: "LLM07: Tool requests credential access"},

		// OWASP LLM08 - Excessive Agency
		{Name: "shell_execution", Type: "content_match", Target: "request", Patterns: []string{
			"(run|execute)\\s+(a\\s+)?(bash|shell|terminal)\\s+(command|script)",
			"bash\\s+-c\\s+",
			"/bin/(ba)?sh\\s+",
		}, Severity: "critical", Action: "block", Description: "LLM08: Shell execution request"},
		{Name: "destructive_file_ops", Type: "content_match", Target: "request", Patterns: []string{
			"rm\\s+(-rf?|--recursive)\\s+/",
			"rm\\s+-rf\\s+\\*",
			"(delete|remove|wipe)\\s+all\\s+(files|data|everything)",
		}, Severity: "critical", Action: "terminate", Description: "LLM08: Destructive file operation"},
		{Name: "privilege_escalation", Type: "content_match", Target: "request", Patterns: []string{
			"sudo\\s+(rm|chmod|chown|kill|bash|sh|python|perl|ruby|apt|yum|dnf|pip|npm|make|gcc|curl|wget)\\b",
			"(run|execute)\\s+(this\\s+)?(command\\s+)?(as|with)\\s+root",
			"(get|gain|obtain)\\s+(root|admin|superuser)\\s+(access|privileges|permissions)",
		}, Severity: "critical", Action: "block", Description: "LLM08: Privilege escalation attempt"},
		{Name: "network_exfiltration", Type: "content_match", Target: "request", Patterns: []string{
			"curl.*\\|\\s*(ba)?sh",
			"wget.*\\|\\s*(ba)?sh",
			"reverse\\s+shell",
		}, Severity: "critical", Action: "terminate", Description: "LLM08: Data exfiltration attempt"},

		// OWASP LLM10 - Model Theft
		{Name: "model_extraction", Type: "content_match", Target: "request", Patterns: []string{
			"(extract|dump|export)\\s+(the\\s+)?(model|weights|parameters)",
			"(what|describe)\\s+(is|are)\\s+your\\s+(weights|parameters|architecture)",
		}, Severity: "warning", Action: "flag", Description: "LLM10: Model extraction attempt"},

		// OWASP LLM06 - Data Exfiltration
		{Name: "bulk_data_extraction", Type: "content_match", Target: "request", Patterns: []string{
			"(list|show|give|dump)\\s+(all\\s+)?(user|customer|employee)\\s+(data|info|records|passwords)",
			"(extract|export|download)\\s+(all\\s+)?(user|database|customer)\\s+(data|records|table)",
			"(get|read|fetch)\\s+(all|every)\\s+(user|customer|account)\\s+from",
		}, Severity: "warning", Action: "flag", Description: "LLM06: Bulk data extraction attempt"},

		// Recursive/Exhaustive Prompts
		{Name: "recursive_prompt", Type: "content_match", Target: "request", Patterns: []string{
			"for\\s+(each|every|all)\\s+(possible\\s+)?(input|combination|permutation)",
			"test\\s+(all|every|each)\\s+(possible\\s+)?(combination|permutation|input)",
			"(exhaustive|brute\\s*force)\\s+(test|search|scan|check)",
			"(iterate|loop)\\s+(through\\s+)?(all|every|each)\\s+(possible|input)",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Recursive/exhaustive prompt detected"},

		// OWASP LLM06 - Sensitive data
		{Name: "credentials_request", Type: "content_match", Target: "request", Patterns: []string{
			"(show|give|list|extract)\\s+(me\\s+)?(the\\s+)?api[_\\s]?key",
			"(show|give|list|extract)\\s+(me\\s+)?(the\\s+)?password",
			"(read|show|cat|display)\\s+(the\\s+)?\\.env\\s+file",
			"(list|show|dump)\\s+(all\\s+)?credentials",
		}, Severity: "warning", Action: "flag", Description: "LLM06: Credentials request"},
		{Name: "pii_ssn", Type: "content_match", Target: "both", Patterns: []string{
			"\\d{3}-\\d{2}-\\d{4}",
		}, Severity: "warning", Action: "flag", Description: "LLM06: SSN pattern detected"},
	}
}

func getStrictPresetRules() []policy.Rule {
	rules := getStandardPresetRules()

	// Add additional strict rules
	rules = append(rules, []policy.Rule{
		{Name: "sql_injection", Type: "content_match", Target: "request", Patterns: []string{
			"drop\\s+(table|database)\\s+",
			";\\s*(drop|delete|truncate|update)\\s+",
			"union\\s+select",
		}, Severity: "critical", Action: "terminate", Description: "SQL injection attempt"},
		{Name: "systematic_probing", Type: "content_match", Target: "request", Patterns: []string{
			"for\\s+(each|every|all)\\s+(possible|input|token|word)",
			"(test|probe|query)\\s+(all|every|each)\\s+(combination|permutation)",
			"(brute|exhaustive)\\s+(force|search|scan)",
		}, Severity: "warning", Action: "flag", Description: "Systematic probing detected"},
	}...)

	return rules
}
