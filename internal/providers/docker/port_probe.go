package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/agent"
)

var portProbeImageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var trustedPortProbeImage = regexp.MustCompile(`^ghcr\.io/eutelic/runnerhub-agent(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|@sha256:[a-f0-9]{64})$`)

// ProbeHostPort checks the host Docker daemon's actual port allocator by
// briefly starting a temporary container with the requested port mapping.
// It uses only the already installed RunnerHub agent image and removes only a
// container carrying this probe's unpredictable ownership label.
func (p *Provider) ProbeHostPort(ctx context.Context, port int) (agent.HostPortProbeResult, error) {
	if port < 1024 || port > 65535 {
		return agent.HostPortProbeResult{}, errors.New("host port is outside the supported range")
	}
	// TrueNAS Custom Apps use Compose-generated container names, so a fixed
	// container named runnerhub-agent cannot be assumed. Resolve the explicitly
	// configured local agent image instead, then run the immutable image ID.
	// Older fixed-name Docker agents remain supported as a fallback.
	imageRef := strings.TrimSpace(p.cfg.PortProbeImage)
	var image string
	var err error
	if imageRef != "" {
		if !trustedPortProbeImage.MatchString(imageRef) {
			return agent.HostPortProbeResult{}, errors.New("host-port probe image must be an official RunnerHub agent reference")
		}
		image, err = p.run.Run(ctx, p.cfg.DockerBinary, "image", "inspect", "--format", "{{.Id}}", imageRef)
	} else {
		image, err = p.run.Run(ctx, p.cfg.DockerBinary, "inspect", "--format", "{{.Image}}", "runnerhub-agent")
	}
	if err != nil || !portProbeImageID.MatchString(strings.TrimSpace(image)) {
		return agent.HostPortProbeResult{}, errors.New("the enrolled RunnerHub Docker agent image could not be verified")
	}
	image = strings.TrimSpace(image)
	for _, hostIP := range []string{"0.0.0.0", "[::]"} {
		status, err := p.probeHostPortMapping(ctx, image, hostIP, port)
		if err != nil {
			return agent.HostPortProbeResult{}, err
		}
		if status == "occupied" {
			return agent.HostPortProbeResult{Port: port, Status: "occupied"}, nil
		}
	}
	return agent.HostPortProbeResult{Port: port, Status: "available"}, nil
}

func (p *Provider) probeHostPortMapping(ctx context.Context, imageID, hostIP string, port int) (string, error) {
	name, err := portProbeName()
	if err != nil {
		return "", errors.New("could not allocate a temporary host-port probe identity")
	}
	portSpec := fmt.Sprintf("%s:%d:%d/tcp", hostIP, port, port)
	_, runErr := p.run.Run(ctx, p.cfg.DockerBinary,
		"run", "--pull", "never", "--rm", "--detach", "--name", name,
		"--label", "runnerhub.host-port-probe="+name,
		"--publish", portSpec, "--entrypoint", "/bin/sh", imageID, "-c", "sleep 60",
	)
	if runErr != nil {
		if err := p.cleanupHostPortProbe(name); err != nil {
			return "", errors.New("temporary host-port probe cleanup could not be confirmed")
		}
		message := strings.ToLower(runErr.Error())
		for _, phrase := range []string{"port is already allocated", "port has already been allocated", "address already in use", "port is already in use", "port already allocated"} {
			if strings.Contains(message, phrase) {
				return "occupied", nil
			}
		}
		return "", errors.New("Docker could not safely verify the requested host-port mapping")
	}
	if err := p.cleanupHostPortProbe(name); err != nil {
		return "", errors.New("temporary host-port probe cleanup could not be confirmed")
	}
	return "available", nil
}

func (p *Provider) cleanupHostPortProbe(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	label, err := p.run.Run(ctx, p.cfg.DockerBinary,
		"inspect", "--format", `{{ index .Config.Labels "runnerhub.host-port-probe" }}`, name)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such object") || strings.Contains(strings.ToLower(err.Error()), "no such container") {
			return nil
		}
		return err
	}
	if strings.TrimSpace(label) != name {
		return errors.New("temporary container ownership label did not match")
	}
	_, err = p.run.Run(ctx, p.cfg.DockerBinary, "rm", "--force", name)
	return err
}

func portProbeName() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "runnerhub-port-probe-" + hex.EncodeToString(raw[:]), nil
}
