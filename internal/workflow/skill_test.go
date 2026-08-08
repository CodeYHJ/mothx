package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureProjectSkillCreatesWorkflowSkill(t *testing.T) {
	root := t.TempDir()
	path, created, err := EnsureProjectSkill(root)
	if err != nil {
		t.Fatalf("EnsureProjectSkill() error = %v", err)
	}
	if !created {
		t.Fatal("expected skill to be created")
	}
	wantPath := filepath.Join(root, ".skills", SkillName, "SKILL.md")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"# Workflow JavaScript",
		"Progressive references",
		"references/00-core-rules.md",
		"references/06-master-slave-team.md",
		"workflow, phase, and agent names must be string literals.",
		"Agent options are prompt, mode, workDir, tools, maxIterations, key",
		"Keep workflows bounded",
		"set timeoutSeconds for long workflow_run calls",
		"Use result, resultKey, resultLatest, results, and log",
		"Use key for repeated logical agents",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skill content missing %q", want)
		}
	}
	corePath := filepath.Join(root, ".skills", SkillName, "references", "00-core-rules.md")
	coreData, err := os.ReadFile(corePath)
	if err != nil {
		t.Fatalf("read core reference: %v", err)
	}
	core := string(coreData)
	for _, want := range []string{
		"Agent names and phase names must be string literals.",
		"workflow(\"auth audit\"",
		"# Core Rules and Skeletons",
		"concurrency is 5",
		"mode and workDir inherit",
		"Defaults: concurrency is 5",
		"maxIterations: 100",
		"resultKey(\"phase.agent\", \"r0\")",
	} {
		if !strings.Contains(core, want) {
			t.Fatalf("core reference missing %q", want)
		}
	}
	for _, rel := range []string{
		"01-research.md",
		"03-decision-routing.md",
		"04-continuous-loops.md",
		"05-horizontal-collaboration.md",
		"07-evaluator-optimizer.md",
		"08-governance-checkpoints.md",
	} {
		if _, err := os.Stat(filepath.Join(root, ".skills", SkillName, "references", rel)); err != nil {
			t.Fatalf("expected reference %s: %v", rel, err)
		}
	}

	loopsData, err := os.ReadFile(filepath.Join(root, ".skills", SkillName, "references", "04-continuous-loops.md"))
	if err != nil {
		t.Fatalf("read loop reference: %v", err)
	}
	loops := string(loopsData)
	for _, want := range []string{
		"# Bounded JavaScript Loops",
		"for (var i = 0; i < 3",
	} {
		if !strings.Contains(loops, want) {
			t.Fatalf("loop reference missing %q", want)
		}
	}

	evaluatorData, err := os.ReadFile(filepath.Join(root, ".skills", SkillName, "references", "07-evaluator-optimizer.md"))
	if err != nil {
		t.Fatalf("read evaluator reference: %v", err)
	}
	evaluator := string(evaluatorData)
	for _, want := range []string{
		"# Evaluator-Optimizer Review Passes",
	} {
		if !strings.Contains(evaluator, want) {
			t.Fatalf("evaluator reference missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"Critic Loops",
		"Bounded Optimizer Loop",
		"(legacy while ",
	} {
		if strings.Contains(evaluator, unwanted) {
			t.Fatalf("evaluator reference should not contain %q", unwanted)
		}
	}
}

func TestEnsureProjectSkillDoesNotOverwriteExistingSkill(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".skills", SkillName)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte("custom workflow skill"), 0644); err != nil {
		t.Fatalf("write custom skill: %v", err)
	}

	gotPath, created, err := EnsureProjectSkill(root)
	if err != nil {
		t.Fatalf("EnsureProjectSkill() error = %v", err)
	}
	if created {
		t.Fatal("did not expect existing skill to be recreated")
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read custom skill: %v", err)
	}
	if string(data) != "custom workflow skill" {
		t.Fatalf("skill was overwritten: %q", string(data))
	}
	refPath := filepath.Join(skillDir, "references", "01-research.md")
	if _, err := os.Stat(refPath); err != nil {
		t.Fatalf("expected missing references to be created: %v", err)
	}
}

func TestEnsureProjectSkillRespectsLowercaseSkill(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".skills", SkillName)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(skillDir, "skill.md")
	if err := os.WriteFile(path, []byte("lowercase workflow skill"), 0644); err != nil {
		t.Fatalf("write lowercase skill: %v", err)
	}

	gotPath, created, err := EnsureProjectSkill(root)
	if err != nil {
		t.Fatalf("EnsureProjectSkill() error = %v", err)
	}
	if created {
		t.Fatal("did not expect lowercase skill to be recreated")
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	refPath := filepath.Join(skillDir, "references", "00-core-rules.md")
	if _, err := os.Stat(refPath); err != nil {
		t.Fatalf("expected missing references to be created: %v", err)
	}
}
