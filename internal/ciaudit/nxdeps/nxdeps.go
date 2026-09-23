package nxdeps

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type graphFile struct {
	Graph struct {
		Nodes map[string]struct {
			Name string `json:"name"`
			Data struct {
				Root string `json:"root"`
			} `json:"data"`
		} `json:"nodes"`
		Dependencies map[string][]struct {
			Source string `json:"source"`
			Target string `json:"target"`
			Type   string `json:"type"`
		} `json:"dependencies"`
	} `json:"graph"`
}

type Graph struct {
	roots   map[string]string
	reverse map[string][]string
}

func Parse(raw []byte) (Graph, error) {
	var doc graphFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Graph{}, fmt.Errorf("parse Nx project graph: %w", err)
	}
	if len(doc.Graph.Nodes) == 0 {
		return Graph{}, fmt.Errorf("Nx project graph has no nodes")
	}
	g := Graph{roots: map[string]string{}, reverse: map[string][]string{}}
	for key, node := range doc.Graph.Nodes {
		name := strings.TrimSpace(node.Name)
		if name == "" {
			name = strings.TrimSpace(key)
		}
		root := normalize(node.Data.Root)
		if name == "" || root == "" {
			return Graph{}, fmt.Errorf("Nx node %q is missing name/root", key)
		}
		g.roots[name] = root
	}
	for source, deps := range doc.Graph.Dependencies {
		if _, ok := g.roots[source]; !ok {
			return Graph{}, fmt.Errorf("Nx dependency source %q is unknown", source)
		}
		for _, dep := range deps {
			target := strings.TrimSpace(dep.Target)
			if _, ok := g.roots[target]; !ok {
				return Graph{}, fmt.Errorf("Nx dependency target %q is unknown", target)
			}
			g.reverse[target] = append(g.reverse[target], source)
		}
	}
	for target := range g.reverse {
		sort.Strings(g.reverse[target])
	}
	return g, nil
}

func normalize(path string) string {
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	path = strings.TrimPrefix(path, "./")
	path = strings.Trim(path, "/")
	return path
}

// AffectedProjects maps changed files to owning Nx projects and then walks
// reverse project dependencies so consumers/tests of a changed library are
// included deterministically. Paths outside every project are returned as
// unknown so callers can fail conservative instead of claiming no impact.
func (g Graph) AffectedProjects(changed []string) (affected []string, unknown []string) {
	set := map[string]struct{}{}
	var queue []string
	for _, raw := range changed {
		path := normalize(raw)
		owned := false
		for project, root := range g.roots {
			if path == root || strings.HasPrefix(path, root+"/") {
				if _, seen := set[project]; !seen {
					set[project] = struct{}{}
					queue = append(queue, project)
				}
				owned = true
			}
		}
		if !owned && path != "" {
			unknown = append(unknown, path)
		}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, dependent := range g.reverse[current] {
			if _, seen := set[dependent]; seen {
				continue
			}
			set[dependent] = struct{}{}
			queue = append(queue, dependent)
		}
	}
	for project := range set {
		affected = append(affected, project)
	}
	sort.Strings(affected)
	sort.Strings(unknown)
	return affected, unknown
}
