package nxdeps

import (
	"reflect"
	"testing"
)

func TestAffectedProjectsWalksReverseDependencies(t *testing.T) {
	raw := []byte(`{
	  "graph": {
	    "nodes": {
	      "web": {"name":"web","data":{"root":"apps/web"}},
	      "api": {"name":"api","data":{"root":"apps/api"}},
	      "shared": {"name":"shared","data":{"root":"libs/shared"}}
	    },
	    "dependencies": {
	      "web": [{"source":"web","target":"shared","type":"static"}],
	      "api": [{"source":"api","target":"shared","type":"static"}],
	      "shared": []
	    }
	  }
	}`)
	graph, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	affected, unknown := graph.AffectedProjects([]string{"libs/shared/src/index.ts"})
	if len(unknown) != 0 {
		t.Fatalf("unknown=%v", unknown)
	}
	if want := []string{"api", "shared", "web"}; !reflect.DeepEqual(affected, want) {
		t.Fatalf("affected=%v want=%v", affected, want)
	}
}

func TestUnknownNxInputFailsConservativeAtCallerBoundary(t *testing.T) {
	raw := []byte(`{"graph":{"nodes":{"web":{"name":"web","data":{"root":"apps/web"}}},"dependencies":{"web":[]}}}`)
	graph, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	affected, unknown := graph.AffectedProjects([]string{"nx.json"})
	if len(affected) != 0 || len(unknown) != 1 || unknown[0] != "nx.json" {
		t.Fatalf("affected=%v unknown=%v", affected, unknown)
	}
}
