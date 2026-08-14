package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestProductionArchitectureGuard prevents adapters from silently reintroducing
// complete Agent construction or canonical Run persistence. The allowlist is
// intentionally small and documents migration ownership rather than package
// convenience.
func TestProductionArchitectureGuard(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture guard")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	var violations []string
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(rel), "internal/agentruntime/") {
			return nil
		}
		fileAST, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		imports := make(map[string]string)
		for _, imp := range fileAST.Imports {
			pathValue := strings.Trim(imp.Path.Value, `"`)
			name := ""
			if imp.Name != nil {
				name = imp.Name.Name
			} else {
				name = filepath.Base(pathValue)
			}
			imports[name] = pathValue
		}
		ast.Inspect(fileAST, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			pkgPath := imports[ident.Name]
			switch {
			case pkgPath == "github.com/startvibecoding/mothx/internal/agent" && (selector.Sel.Name == "New" || selector.Sel.Name == "NewWithLoopConfig"):
				violations = append(violations, fmt.Sprintf("%s: direct %s.%s; use SessionRuntime.BuildAgent/BuildTransientAgent", rel, ident.Name, selector.Sel.Name))
			case pkgPath == "github.com/startvibecoding/mothx/internal/session" && isCanonicalRunPersistence(selector.Sel.Name) && !isLegacyRunPersistenceOwner(rel):
				violations = append(violations, fmt.Sprintf("%s: direct session.%s; use ExecutionRuntime/RunStore", rel, selector.Sel.Name))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("production architecture violations:\n- %s", strings.Join(violations, "\n- "))
	}
}

func isCanonicalRunPersistence(name string) bool {
	switch name {
	case "SaveSessionRun", "UpdateSessionRunStatus":
		return true
	default:
		return false
	}
}

func isLegacyRunPersistenceOwner(rel string) bool {
	rel = filepath.ToSlash(rel)
	return strings.HasPrefix(rel, "internal/agentruntime/") ||
		strings.HasPrefix(rel, "internal/cron/") ||
		rel == "internal/serve/openaiapi/run_manager.go" ||
		rel == "internal/serve/openaiapi/background_run_coordinator.go"
}
