package godeps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Graph is a deterministic mapping from a logical CI target to repository files
// that can affect it according to the Go toolchain's package, test, and embed
// dependency graph.
type Graph struct {
	root    string
	targets map[string]map[string]struct{}
}

type listedPackage struct {
	Dir             string
	GoFiles         []string
	CgoFiles        []string
	CFiles          []string
	CXXFiles        []string
	MFiles          []string
	HFiles          []string
	SFiles          []string
	SwigFiles       []string
	SwigCXXFiles    []string
	SysoFiles       []string
	EmbedFiles      []string
	TestGoFiles     []string
	XTestGoFiles    []string
	TestEmbedFiles  []string
	XTestEmbedFiles []string
}

// TargetSpec is the execution context that can change a Go dependency graph.
type TargetSpec struct {
	Patterns  []string
	GOOS      string
	GOARCH    string
	BuildTags []string
}

// Load asks the Go toolchain for transitive package/test/embed inputs using the
// host build context.
func Load(ctx context.Context, repoRoot string, targets map[string][]string) (Graph, error) {
	specs := make(map[string]TargetSpec, len(targets))
	for name, patterns := range targets {
		specs[name] = TargetSpec{Patterns: patterns}
	}
	return LoadSpecs(ctx, repoRoot, specs)
}

// LoadSpecs evaluates each target under its declared GOOS/GOARCH/build-tag
// context. This prevents a dependency proof from silently reusing the host
// platform's graph for a cross-build job.
func LoadSpecs(ctx context.Context, repoRoot string, targets map[string]TargetSpec) (Graph, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return Graph{}, err
	}
	g := Graph{root: root, targets: make(map[string]map[string]struct{}, len(targets))}
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := targets[name]
		if len(spec.Patterns) == 0 {
			return Graph{}, fmt.Errorf("target %q has no Go patterns", name)
		}
		args := []string{"list", "-deps", "-test", "-json", "-buildvcs=false"}
		if len(spec.BuildTags) > 0 {
			tags := append([]string(nil), spec.BuildTags...)
			sort.Strings(tags)
			args = append(args, "-tags", strings.Join(tags, ","))
		}
		args = append(args, spec.Patterns...)
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = root
		cmd.Env = os.Environ()
		if strings.TrimSpace(spec.GOOS) != "" {
			cmd.Env = append(cmd.Env, "GOOS="+strings.TrimSpace(spec.GOOS))
		}
		if strings.TrimSpace(spec.GOARCH) != "" {
			cmd.Env = append(cmd.Env, "GOARCH="+strings.TrimSpace(spec.GOARCH))
		}
		out, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return Graph{}, fmt.Errorf("go list target %q: %s", name, strings.TrimSpace(string(exit.Stderr)))
			}
			return Graph{}, fmt.Errorf("go list target %q: %w", name, err)
		}
		inputs := map[string]struct{}{"go.mod": {}, "go.sum": {}}
		dec := json.NewDecoder(strings.NewReader(string(out)))
		for {
			var pkg listedPackage
			if err := dec.Decode(&pkg); err != nil {
				if err == io.EOF {
					break
				}
				return Graph{}, fmt.Errorf("decode go list target %q: %w", name, err)
			}
			if err := addPackageInputs(root, inputs, pkg); err != nil {
				return Graph{}, err
			}
		}
		g.targets[name] = inputs
	}
	return g, nil
}

func addPackageInputs(root string, inputs map[string]struct{}, pkg listedPackage) error {
	dir, err := filepath.Abs(pkg.Dir)
	if err != nil {
		return err
	}
	relDir, err := filepath.Rel(root, dir)
	if err != nil || relDir == ".." || strings.HasPrefix(relDir, ".."+string(filepath.Separator)) {
		return nil
	}
	files := [][]string{
		pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles, pkg.HFiles,
		pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles, pkg.SysoFiles, pkg.EmbedFiles,
		pkg.TestGoFiles, pkg.XTestGoFiles, pkg.TestEmbedFiles, pkg.XTestEmbedFiles,
	}
	for _, group := range files {
		for _, file := range group {
			rel := filepath.ToSlash(filepath.Clean(filepath.Join(relDir, file)))
			rel = strings.TrimPrefix(rel, "./")
			if rel != "" && rel != "." {
				inputs[rel] = struct{}{}
			}
		}
	}
	return nil
}

// Affected returns targets whose proven transitive input set intersects changed.
// Unknown/non-Go files are not guessed here; callers should fail conservative
// for inputs outside the graph.
func (g Graph) Affected(changed []string) map[string]bool {
	out := make(map[string]bool, len(g.targets))
	for name, inputs := range g.targets {
		for _, raw := range changed {
			path := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
			path = strings.TrimPrefix(path, "./")
			if _, ok := inputs[path]; ok {
				out[name] = true
				break
			}
		}
	}
	return out
}

// Inputs exposes a sorted copy for evidence/debugging.
func (g Graph) Inputs(target string) []string {
	set := g.targets[target]
	out := make([]string, 0, len(set))
	for path := range set {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
