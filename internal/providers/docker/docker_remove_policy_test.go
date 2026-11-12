package docker

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// Every Docker container removal in the provider and cgroup capability probe must include -v.
// DinD images declare /var/lib/docker as an anonymous volume; omitting -v
// during startup rollback leaves build data behind even when the worker
// never successfully starts.
func TestDockerRemoveCallsAlwaysReclaimAnonymousVolumes(t *testing.T) {
	fset := token.NewFileSet()
	removed := 0
	for _, source := range []string{"docker.go", "cgroup.go"} {
		file, err := parser.ParseFile(fset, source, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Run" {
				return true
			}

			rmIndex := -1
			for i, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err == nil && value == "rm" {
					rmIndex = i
					break
				}
			}
			if rmIndex < 0 {
				return true
			}
			removed++
			hasForce, hasVolumes := false, false
			for _, arg := range call.Args[rmIndex+1:] {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				if value == "-f" {
					hasForce = true
				}
				if value == "-v" {
					hasVolumes = true
				}
			}
			if !hasForce || !hasVolumes {
				t.Errorf("%s: Docker rm must use -f -v (force=%t, removeAnonymousVolumes=%t)", fset.Position(call.Pos()), hasForce, hasVolumes)
			}
			return true
		})
	}
	if removed < 9 {
		t.Fatalf("checked %d Docker removal paths, want at least 9", removed)
	}
}
