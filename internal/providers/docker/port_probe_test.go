package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestProbeHostPortUsesOnlyOwnedTemporaryMappingsForBothAddressFamilies(t *testing.T) {
	const port = 8790
	imageID := "sha256:" + strings.Repeat("a", 64)
	var mappings []string
	var labels []string
	var names []string
	runner := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		if len(args) == 0 {
			t.Fatal("empty Docker invocation")
		}
		switch args[0] {
		case "inspect":
			if len(args) > 2 && args[len(args)-1] == "runnerhub-agent" {
				return imageID, nil
			}
			if len(args) > 3 && strings.Contains(args[2], "runnerhub.host-port-probe") {
				return args[3], nil
			}
			return "", errors.New("unexpected inspect")
		case "run":
			name := args[6]
			label := args[8]
			if !strings.HasPrefix(name, "runnerhub-port-probe-") || label != "runnerhub.host-port-probe="+name {
				t.Fatalf("probe container lacks unique owner identity: %#v", args)
			}
			mapping := args[10]
			mappings = append(mappings, mapping)
			labels = append(labels, label)
			names = append(names, name)
			if args[1] != "--pull" || args[2] != "never" || args[4] != "--detach" ||
				args[11] != "--entrypoint" || args[12] != "/bin/sh" || args[14] != "-c" || args[15] != "sleep 60" {
				t.Fatalf("probe did not use a bounded local image-only container: %#v", args)
			}
			return "temporary-id", nil
		case "rm":
			if len(args) != 3 || args[1] != "--force" {
				t.Fatalf("unexpected cleanup args: %#v", args)
			}
			owned := false
			for _, name := range names {
				if args[2] == name {
					owned = true
					break
				}
			}
			if !owned {
				t.Fatalf("cleanup targeted an unowned container: %#v", args)
			}
			return "", nil
		default:
			t.Fatalf("unexpected Docker command: %#v", args)
			return "", nil
		}
	}}
	p, err := newWithRunner(Config{DockerBinary: "docker", StateDir: t.TempDir()}, runner)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ProbeHostPort(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "available" || got.Port != port {
		t.Fatalf("probe result = %+v", got)
	}
	if strings.Join(mappings, ",") != "0.0.0.0:8790:8790/tcp,[::]:8790:8790/tcp" {
		t.Fatalf("tested mappings = %v", mappings)
	}
	if len(labels) != 2 || len(runner.calls) != 7 {
		t.Fatalf("probe/cleanup command count: labels=%d calls=%d", len(labels), len(runner.calls))
	}
}

func TestProbeHostPortReportsBindConflictAndCleansOnlyItsOwnFailedProbe(t *testing.T) {
	const port = 8790
	imageID := "sha256:" + strings.Repeat("b", 64)
	runner := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		switch args[0] {
		case "inspect":
			if args[len(args)-1] == "runnerhub-agent" {
				return imageID, nil
			}
			return "", errors.New("No such container")
		case "run":
			return "", errors.New("docker: Error response from daemon: driver failed programming external connectivity: Bind for 0.0.0.0:8790 failed: port is already allocated")
		case "rm":
			t.Fatalf("must not remove a container that Docker failed to create: %#v", args)
			return "", nil
		default:
			t.Fatalf("unexpected command: %#v", args)
			return "", nil
		}
	}}
	p, err := newWithRunner(Config{DockerBinary: "docker", StateDir: t.TempDir()}, runner)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ProbeHostPort(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "occupied" || got.Port != port {
		t.Fatalf("probe result = %+v", got)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("expected image verify, failed run, and owned-container absence check; got %d calls", len(runner.calls))
	}
}

func TestProbeHostPortFailsClosedWhenProbeCannotBeVerifiedOrCleaned(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("c", 64)
	for _, tc := range []struct {
		name string
		fn   func(string, ...string) (string, error)
	}{
		{
			name: "invalid agent image",
			fn: func(_ string, args ...string) (string, error) {
				if args[0] == "inspect" {
					return "runnerhub-agent:latest", nil
				}
				t.Fatalf("unexpected command after invalid image: %#v", args)
				return "", nil
			},
		},
		{
			name: "unknown docker error",
			fn: func(_ string, args ...string) (string, error) {
				switch args[0] {
				case "inspect":
					if args[len(args)-1] == "runnerhub-agent" {
						return imageID, nil
					}
					return "", errors.New("No such container")
				case "run":
					return "", errors.New("Docker daemon unavailable")
				default:
					t.Fatalf("unexpected command: %#v", args)
					return "", nil
				}
			},
		},
		{
			name: "cleanup cannot verify ownership",
			fn: func(_ string, args ...string) (string, error) {
				switch args[0] {
				case "inspect":
					if args[len(args)-1] == "runnerhub-agent" {
						return imageID, nil
					}
					return "different-probe", nil
				case "run":
					return "temporary-id", nil
				default:
					t.Fatalf("unexpected command: %#v", args)
					return "", nil
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := newWithRunner(Config{DockerBinary: "docker", StateDir: t.TempDir()}, &fakeRunner{fn: tc.fn})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.ProbeHostPort(context.Background(), 8790); err == nil {
				t.Fatal("expected fail-closed host-port result")
			}
		})
	}
}

func TestProbeHostPortRejectsUnsupportedPortsBeforeDockerAccess(t *testing.T) {
	for _, port := range []int{-1, 0, 80, 1023, 65536, 70000} {
		t.Run(fmt.Sprintf("port-%d", port), func(t *testing.T) {
			runner := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
				t.Fatalf("invalid port %d reached Docker: %#v", port, args)
				return "", nil
			}}
			p, err := newWithRunner(Config{DockerBinary: "docker", StateDir: t.TempDir()}, runner)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.ProbeHostPort(context.Background(), port); err == nil {
				t.Fatalf("port %d should fail closed", port)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("invalid port %d invoked Docker", port)
			}
		})
	}
}

func TestProbeHostPortUsesConfiguredLocalImageForComposeNamedAgent(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("d", 64)
	ref := "ghcr.io/eutelic/runnerhub-agent:v1.0.0"
	var inspected bool
	runner := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
		switch args[0] {
		case "image":
			if len(args) != 5 || args[1] != "inspect" || args[2] != "--format" || args[3] != "{{.Id}}" || args[4] != ref {
				t.Fatalf("unexpected local image resolution: %#v", args)
			}
			inspected = true
			return imageID, nil
		case "run":
			if !inspected || args[13] != imageID {
				t.Fatalf("probe did not use verified local image ID: %#v", args)
			}
			return "temporary-id", nil
		case "inspect":
			return args[len(args)-1], nil
		case "rm":
			return "", nil
		default:
			t.Fatalf("unexpected Docker command: %#v", args)
			return "", nil
		}
	}}
	p, err := newWithRunner(Config{DockerBinary: "docker", StateDir: t.TempDir(), PortProbeImage: ref}, runner)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ProbeHostPort(context.Background(), 8790)
	if err != nil || got.Status != "available" || !inspected {
		t.Fatalf("configured image probe: got=%+v err=%v inspected=%v", got, err, inspected)
	}
}

func TestProbeHostPortRejectsUntrustedConfiguredImageBeforeDocker(t *testing.T) {
	for _, imageRef := range []string{"alpine:latest", "ghcr.io/eutelic/runnerhub-agent:latest --privileged", "ghcr.io/eutelic/runnerhub-agent@"} {
		runner := &fakeRunner{fn: func(_ string, args ...string) (string, error) {
			t.Fatalf("untrusted port-probe image reached Docker: %#v", args)
			return "", nil
		}}
		p, err := newWithRunner(Config{DockerBinary: "docker", StateDir: t.TempDir(), PortProbeImage: imageRef}, runner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.ProbeHostPort(context.Background(), 8790); err == nil {
			t.Fatalf("untrusted image %q should fail closed", imageRef)
		}
	}
}
