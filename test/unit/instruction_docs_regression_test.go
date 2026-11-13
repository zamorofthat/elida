package unit

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"elida/internal/config"
	"elida/internal/instruction"
)

// These tests run this repository's own documentation through instruction
// integrity with the shipped default rules. Failures name the file and the
// rule, never the matched text.

// defaultInstructionRegistry is a registry over the shipped default rules,
// with the shipped shape-detection settings.
func defaultInstructionRegistry(t *testing.T) (*instruction.Registry, config.InstructionIntegrityConfig) {
	t.Helper()
	cfg := config.DefaultConfig().Policy.InstructionIntegrity
	rules := make([]instruction.Rule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		rules = append(rules, instruction.Rule{Name: r.Name, Patterns: r.Patterns, Severity: r.Severity, Action: r.Action})
	}
	if len(rules) == 0 {
		t.Fatal("no default instruction rules")
	}
	scanner, err := instruction.NewScanner(rules)
	if err != nil {
		t.Fatalf("NewScanner: %v", err)
	}
	reg := instruction.NewRegistry(scanner, &memStore{files: make(map[string]*instruction.Record)}, 1000)
	t.Cleanup(reg.Stop)
	return reg, cfg
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// repoMarkdown returns the repository's documentation, keyed by its path
// relative to the repository root. docs/superpowers is gitignored working
// material and is absent in CI.
func repoMarkdown(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	docs := make(map[string]string)
	read := func(rel string) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		docs[filepath.ToSlash(rel)] = string(b)
	}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if filepath.ToSlash(rel) == "docs/superpowers" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			read(rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	for _, rel := range []string{"CHANGELOG.md", "README.md", "SECURITY.md", "deploy/helm/elida/README.md"} {
		read(rel)
	}
	if len(docs) < 20 {
		t.Fatalf("found %d documentation files, want at least 20: an empty walk must not pass", len(docs))
	}
	return docs
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// blockingRules returns the names of the rules a result blocks on.
func blockingRules(result instruction.ScanResult) []string {
	var names []string
	for _, v := range result.Violations {
		if v.Action == "block" {
			names = append(names, v.RuleName)
		}
	}
	return names
}

func TestRepoMarkdownNeverBlocks(t *testing.T) {
	reg, cfg := defaultInstructionRegistry(t)
	docs := repoMarkdown(t)

	const claudeMD = "Contents of /repo/CLAUDE.md:\n\nUse gofmt.\n\n"
	claudeHash := ""
	behindBlocks := 0

	for _, rel := range sortedKeys(docs) {
		body := docs[rel]
		marker := "Contents of /repo/" + rel + ":\n\n"

		// Under its own marker a document is a document: nothing is tracked.
		if files := instruction.ExtractAll(marker+body, cfg.ShapeDetection, cfg.ShapeConfidenceThreshold); len(files) != 0 {
			t.Errorf("%s (own marker): tracked as %d instruction file(s), want none", rel, len(files))
		}

		// With no marker, shape detection may track it but it must not block.
		for _, file := range instruction.ExtractAll(body, cfg.ShapeDetection, cfg.ShapeConfidenceThreshold) {
			if result := reg.Check("unmarked", file); result.ShouldBlock {
				t.Errorf("%s (unmarked): blocks on %v", rel, blockingRules(result))
			}
		}

		// Behind a tracked file in the same block it is recorded under its
		// own path and never leaks into the tracked file's record. It is
		// enforced like that file, though: a document in an instruction
		// block cannot be told from text below a marker-shaped line written
		// into CLAUDE.md (TestMarkerLineCannotDowngradeEnforcement), so one
		// that quotes a blocked pattern is refused there, as it was before
		// blocks were split. Which ones do is logged, not asserted.
		files := instruction.ExtractAll(claudeMD+marker+body, cfg.ShapeDetection, cfg.ShapeConfidenceThreshold)
		if len(files) != 2 || files[0].SourcePath != "/repo/CLAUDE.md" || files[1].SourcePath != "/repo/"+rel {
			t.Errorf("%s (behind CLAUDE.md): want /repo/CLAUDE.md then /repo/%s, got %d file(s)", rel, rel, len(files))
			continue
		}
		if claudeHash == "" {
			claudeHash = files[0].Hash
		}
		if files[0].Hash != claudeHash {
			t.Errorf("%s (behind CLAUDE.md): the CLAUDE.md hash changed with the document behind it", rel)
		}
		if result := reg.Check("behind", files[0]); result.ShouldBlock {
			t.Errorf("%s (behind CLAUDE.md): the CLAUDE.md record blocks on %v", rel, blockingRules(result))
		}
		if result := reg.Check("behind", files[1]); result.ShouldBlock {
			behindBlocks++
			t.Logf("%s (behind CLAUDE.md): would be refused on %v", rel, blockingRules(result))
		}
	}
	t.Logf("%d of %d documents would be refused if imported into an instruction block", behindBlocks, len(docs))
}

// TestReportedReminderDoesNotBlock is the report this work started from: a
// reminder block carrying CLAUDE.md, the agent guide it imports and the memory
// index was refused because `eval\s*\(` matched "Retrieval (".
func TestReportedReminderDoesNotBlock(t *testing.T) {
	reg, cfg := defaultInstructionRegistry(t)
	const block = "As you answer, you can use the following context:\n" +
		"Contents of /repo/CLAUDE.md (project instructions, checked into the codebase):\n\n" +
		"Read the guide.\n\n" +
		"Contents of /repo/.entire/agent-guide.md (project instructions, checked into the codebase):\n\n" +
		"Retrieval (query takes --json)\n\n" +
		"Contents of /home/u/.claude/projects/x/memory/MEMORY.md (user's auto-memory, persists across conversations):\n\n" +
		"- [Note](note.md) — hook\n"

	files := instruction.ExtractAll(block, cfg.ShapeDetection, cfg.ShapeConfidenceThreshold)
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3", len(files))
	}
	for _, file := range files {
		result := reg.Check("reported", file)
		if result.ShouldBlock {
			t.Errorf("%s blocks on %v", file.SourcePath, blockingRules(result))
		}
		if file.SourcePath == "/repo/.entire/agent-guide.md" && len(result.Violations) != 0 {
			t.Errorf("%s has %d violation(s), want none", file.SourcePath, len(result.Violations))
		}
	}
}

// TestRepoInstructionFilesKnownResidual pins what is still true after the
// noise-floor work, so that changing it is deliberate. .ai/AGENTS.md is a
// tracked instruction file that documents a blocked pattern (its detection
// table names exec()), and it still blocks under enforce. It stops when the
// instruction provenance plan (plan 2) narrows the 403 to hidden content and
// pipe-to-shell and moves everything else onto the risk ladder.
func TestRepoInstructionFilesKnownResidual(t *testing.T) {
	reg, cfg := defaultInstructionRegistry(t)
	root := repoRoot(t)

	check := func(name string) instruction.ScanResult {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, ".ai", name))
		if err != nil {
			t.Fatalf("read .ai/%s: %v", name, err)
		}
		files := instruction.ExtractAll("Contents of /repo/.ai/"+name+":\n\n"+string(b), cfg.ShapeDetection, cfg.ShapeConfidenceThreshold)
		if len(files) != 1 || files[0].Confidence != instruction.ConfidenceHigh {
			t.Fatalf(".ai/%s: got %d file(s), want one at high confidence", name, len(files))
		}
		return reg.Check("residual", files[0])
	}

	if result := check("CLAUDE.md"); len(result.Violations) != 0 {
		t.Errorf(".ai/CLAUDE.md: %d violation(s), want none (blocking: %v)", len(result.Violations), blockingRules(result))
	}

	result := check("AGENTS.md")
	if blocking := blockingRules(result); len(blocking) != 1 || blocking[0] != "instruction_shell_exec" || !result.ShouldBlock {
		t.Errorf(".ai/AGENTS.md: blocks on %v (ShouldBlock %v), want exactly [instruction_shell_exec]", blocking, result.ShouldBlock)
	}
}

// TestRepoMarkdownShapeFloor bounds how much ordinary documentation shape
// detection tracks. Measured when written: 1 of 26 (docs/index.md, whose
// subject is agents, so agent vocabulary is topical there). The scorer this
// replaced tracked 6. The bound is 2 so an ordinary docs edit does not turn
// the suite red; raise it only with a measurement in the commit message.
func TestRepoMarkdownShapeFloor(t *testing.T) {
	_, cfg := defaultInstructionRegistry(t)
	docs := repoMarkdown(t)

	var tracked []string
	for _, rel := range sortedKeys(docs) {
		if len(instruction.ExtractAll(docs[rel], cfg.ShapeDetection, cfg.ShapeConfidenceThreshold)) > 0 {
			tracked = append(tracked, rel)
		}
	}
	if len(tracked) > 2 {
		t.Errorf("shape detection tracks %d of %d documentation files, want at most 2: %v", len(tracked), len(docs), tracked)
	}
	t.Logf("shape detection tracks %d of %d: %v", len(tracked), len(docs), tracked)
}
