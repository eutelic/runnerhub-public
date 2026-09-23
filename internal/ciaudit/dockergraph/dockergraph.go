package dockergraph

import (
	"bufio"
	"fmt"
	"strings"
)

// Graph models local stage dependencies and direct build-context COPY inputs.
// Unsupported dynamic/JSON COPY forms fail closed during parsing.
type Graph struct {
	stages map[string]*Stage
	order  []string
}

type Stage struct {
	Name       string
	Base       string
	FromStages []string
	Inputs     []string
}

func Parse(dockerfile string) (Graph, error) {
	g := Graph{stages: map[string]*Stage{}}
	var current *Stage
	scanner := bufio.NewScanner(strings.NewReader(dockerfile))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "FROM ") {
			fields := strings.Fields(line)
			if len(fields) < 4 || !strings.EqualFold(fields[len(fields)-2], "AS") {
				return Graph{}, fmt.Errorf("every Dockerfile stage must have a stable AS name: %q", line)
			}
			name := fields[len(fields)-1]
			base := fields[1]
			current = &Stage{Name: name, Base: base}
			if _, exists := g.stages[name]; exists {
				return Graph{}, fmt.Errorf("duplicate Dockerfile stage %q", name)
			}
			if _, local := g.stages[base]; local {
				current.FromStages = append(current.FromStages, base)
			}
			g.stages[name] = current
			g.order = append(g.order, name)
			continue
		}
		if current == nil || !strings.HasPrefix(upper, "COPY ") {
			continue
		}
		if strings.Contains(line, "[") {
			return Graph{}, fmt.Errorf("JSON COPY form is unsupported for deterministic analysis: %q", line)
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return Graph{}, fmt.Errorf("invalid COPY: %q", line)
		}
		var from string
		var args []string
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "--from=") {
				from = strings.TrimPrefix(field, "--from=")
				continue
			}
			if strings.HasPrefix(field, "--") {
				continue
			}
			args = append(args, field)
		}
		if from != "" {
			if _, local := g.stages[from]; local {
				current.FromStages = append(current.FromStages, from)
			}
			continue
		}
		if len(args) < 2 {
			return Graph{}, fmt.Errorf("COPY missing source/destination: %q", line)
		}
		for _, source := range args[:len(args)-1] {
			source = normalize(source)
			if source != "" {
				current.Inputs = append(current.Inputs, source)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Graph{}, err
	}
	return g, nil
}

func normalize(path string) string {
	path = strings.TrimSpace(strings.TrimPrefix(path, "./"))
	path = strings.TrimPrefix(path, "/")
	return path
}

func matches(source, changed string) bool {
	source = normalize(source)
	changed = normalize(changed)
	if source == "" {
		return false
	}
	if source == "." || source == "*" {
		return true
	}
	if strings.HasSuffix(source, "/") {
		return strings.HasPrefix(changed, source)
	}
	return changed == source || strings.HasPrefix(changed, source+"/")
}

// AffectedTargets reports whether each requested final stage is invalidated by
// the changed repository paths through direct COPY or local-stage ancestry.
func (g Graph) AffectedTargets(changed []string, targets ...string) (map[string]bool, error) {
	memo := map[string]bool{}
	visiting := map[string]bool{}
	var affected func(string) (bool, error)
	affected = func(name string) (bool, error) {
		if value, ok := memo[name]; ok {
			return value, nil
		}
		stage, ok := g.stages[name]
		if !ok {
			return false, fmt.Errorf("unknown Dockerfile stage %q", name)
		}
		if visiting[name] {
			return false, fmt.Errorf("Dockerfile stage dependency cycle at %q", name)
		}
		visiting[name] = true
		defer delete(visiting, name)
		for _, input := range stage.Inputs {
			for _, path := range changed {
				if matches(input, path) {
					memo[name] = true
					return true, nil
				}
			}
		}
		for _, parent := range stage.FromStages {
			value, err := affected(parent)
			if err != nil {
				return false, err
			}
			if value {
				memo[name] = true
				return true, nil
			}
		}
		memo[name] = false
		return false, nil
	}
	out := make(map[string]bool, len(targets))
	for _, target := range targets {
		value, err := affected(target)
		if err != nil {
			return nil, err
		}
		out[target] = value
	}
	return out, nil
}
