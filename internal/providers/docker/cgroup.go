package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/provider"
)

const aggregateResourceFeature = "aggregateResourceGroups"

type aggregateState struct {
	mu        sync.Mutex
	checked   bool
	supported bool
	version   string
	driver    string
	reason    string
	cpu       map[string]cpuUsageSample
}

type cpuUsageSample struct {
	usageUsec int64
	at        time.Time
}

type aggregateGroup struct {
	Driver       string `json:"driver"`
	Parent       string `json:"parent"`
	RelativePath string `json:"relativePath"`
	KeeperUnit   string `json:"keeperUnit,omitempty"`
}

func newAggregateState() *aggregateState {
	return &aggregateState{cpu: map[string]cpuUsageSample{}}
}

func (p *Provider) aggregateResourceGroupCapabilities(ctx context.Context) (supported bool, version, driver, reason string) {
	p.aggregate.mu.Lock()
	defer p.aggregate.mu.Unlock()
	if p.aggregate.checked {
		return p.aggregate.supported, p.aggregate.version, p.aggregate.driver, p.aggregate.reason
	}
	p.aggregate.checked = true
	p.aggregate.supported, p.aggregate.version, p.aggregate.driver, p.aggregate.reason = p.probeAggregateResourceGroups(ctx)
	return p.aggregate.supported, p.aggregate.version, p.aggregate.driver, p.aggregate.reason
}

func (p *Provider) disableAggregateResourceGroups() {
	p.aggregate.mu.Lock()
	p.aggregate.checked = true
	p.aggregate.supported = false
	p.aggregate.reason = "runtime_containment_lost"
	p.aggregate.mu.Unlock()
}

func (p *Provider) probeAggregateResourceGroups(ctx context.Context) (bool, string, string, string) {
	if !p.cfg.AllowPrivilegedWorkers {
		return false, "", "", "privileged_workers_disabled"
	}
	out, err := p.run.Run(ctx, p.cfg.DockerBinary, "info", "--format", "{{.CgroupVersion}}|{{.CgroupDriver}}")
	if err != nil {
		return false, "", "", "docker_info_unavailable"
	}
	parts := strings.SplitN(strings.TrimSpace(out), "|", 2)
	if len(parts) != 2 {
		return false, "", "", "docker_cgroup_info_invalid"
	}
	version, driver := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if version != "2" {
		return false, version, driver, "cgroup_v2_required"
	}
	if driver != "cgroupfs" && driver != "systemd" {
		return false, version, driver, "unsupported_cgroup_driver"
	}
	if !p.cgroupRootReadable() {
		return false, version, driver, "host_cgroup_inaccessible"
	}

	resources := core.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30}
	var group aggregateGroup
	switch driver {
	case "cgroupfs":
		group, err = p.prepareCgroupFSGroup("probe-"+strconv.FormatInt(time.Now().UnixNano(), 36), resources)
	case "systemd":
		group, err = p.prepareSystemdGroup(ctx, "probe"+strconv.FormatInt(time.Now().UnixNano(), 36), resources)
	}
	if err != nil {
		return false, version, driver, "parent_creation_failed"
	}
	defer p.cleanupAggregateGroup(context.Background(), group)

	if err := p.verifyAggregateLimits(group, resources); err != nil {
		return false, version, driver, "parent_limits_unverified"
	}

	runDir, err := os.MkdirTemp(p.cfg.StateDir, "aggregate-cgroup-probe-")
	if err != nil {
		return false, version, driver, "probe_state_unavailable"
	}
	defer os.RemoveAll(runDir)
	name := "runnerhub-cgroup-probe-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	defer func() {
		_, _ = p.run.Run(context.Background(), p.cfg.DockerBinary, "rm", "-f", "-v", name)
	}()

	_, err = p.run.Run(ctx, p.cfg.DockerBinary,
		"run", "-d", "--name", name,
		"--privileged",
		"--cgroup-parent", group.Parent,
		"--mount", "type=bind,src="+runDir+",dst="+dindSocketDir,
		p.cfg.DinDImage,
		"dockerd",
		"--host=unix://"+dindSocketPath,
		"--registry-mirror=https://mirror.gcr.io",
	)
	if err != nil {
		return false, version, driver, "probe_sidecar_start_failed"
	}
	if err := p.waitForPrivateDocker(ctx, name); err != nil {
		return false, version, driver, "probe_private_docker_unavailable"
	}
	if err := p.verifyContainerCgroupParent(ctx, name, group.Parent); err != nil {
		return false, version, driver, "probe_parent_mismatch"
	}

	// Docker defaults to a private cgroup namespace on cgroup v2. The DinD
	// sidecar therefore sees its own cgroup as "/", while a nested workload
	// must appear below it. That proves the nested process remains a descendant
	// of the already-verified bounded parent instead of escaping the budget.
	nested, err := p.run.Run(ctx, p.cfg.DockerBinary,
		"exec", "-e", "DOCKER_HOST=unix://"+dindSocketPath, name,
		"docker", "run", "--rm", "--pull", "always",
		"--entrypoint", "/bin/cat", p.cfg.DinDImage, "/proc/self/cgroup",
	)
	if err != nil {
		return false, version, driver, "nested_probe_failed"
	}
	path := unifiedCgroupPath(nested)
	if path == "" || path == "/" {
		return false, version, driver, "nested_cgroup_not_contained"
	}
	return true, version, driver, ""
}

func (p *Provider) cgroupRootReadable() bool {
	if p.cfg.CgroupRoot == "" {
		return false
	}
	available, err := os.ReadFile(filepath.Join(p.cfg.CgroupRoot, "cgroup.controllers"))
	if err != nil {
		return false
	}
	hasCPU, hasMemory := false, false
	for _, field := range strings.Fields(string(available)) {
		switch field {
		case "cpu":
			hasCPU = true
		case "memory":
			hasMemory = true
		}
	}
	return hasCPU && hasMemory
}

func (p *Provider) prepareCgroupFSGroup(name string, resources core.Resources) (aggregateGroup, error) {
	root := p.cfg.CgroupRoot
	if err := enableControllers(root); err != nil {
		return aggregateGroup{}, err
	}
	base := filepath.Join(root, "runnerhub")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return aggregateGroup{}, err
	}
	if err := enableControllers(base); err != nil {
		return aggregateGroup{}, err
	}
	groupName := "worker-" + safeID(name)
	groupPath := filepath.Join(base, groupName)
	if err := os.MkdirAll(groupPath, 0o755); err != nil {
		return aggregateGroup{}, err
	}
	if err := enableControllers(groupPath); err != nil {
		removeEmptyCgroupTree(groupPath)
		return aggregateGroup{}, err
	}
	if err := writeCgroupLimit(groupPath, resources); err != nil {
		removeEmptyCgroupTree(groupPath)
		return aggregateGroup{}, err
	}
	return aggregateGroup{
		Driver:       "cgroupfs",
		Parent:       "/runnerhub/" + groupName,
		RelativePath: "runnerhub/" + groupName,
	}, nil
}

func (p *Provider) prepareSystemdGroup(ctx context.Context, name string, resources core.Resources) (aggregateGroup, error) {
	unitStem := "runnerhubw" + strings.ReplaceAll(safeID(name), "-", "")
	if unitStem == "runnerhubw" {
		return aggregateGroup{}, fmt.Errorf("invalid systemd aggregate group name")
	}
	slice := unitStem + ".slice"
	keeper := unitStem + "keeper.service"

	// systemd owns the cgroup tree when Docker uses its systemd driver. Create a
	// transient no-process keeper service in the target slice so PID 1 creates
	// and owns the slice, then apply limits through systemctl instead of writing
	// to systemd-managed cgroup files behind its back.
	if _, err := p.hostSystemd(ctx, "systemd-run",
		"--quiet",
		"--unit="+keeper,
		"--slice="+slice,
		"--property=RemainAfterExit=yes",
		"/bin/true",
	); err != nil {
		return aggregateGroup{}, fmt.Errorf("create aggregate systemd slice: %w", err)
	}
	cleanup := func() {
		_, _ = p.hostSystemd(context.Background(), "systemctl", "stop", slice)
	}

	args := []string{"set-property", "--runtime", slice}
	if resources.CPUMillis > 0 {
		args = append(args, "CPUQuota="+formatCPUQuota(resources.CPUMillis))
	}
	if resources.MemoryBytes > 0 {
		args = append(args, "MemoryMax="+strconv.FormatInt(resources.MemoryBytes, 10))
	}
	if len(args) > 3 {
		if _, err := p.hostSystemd(ctx, "systemctl", args...); err != nil {
			cleanup()
			return aggregateGroup{}, fmt.Errorf("limit aggregate systemd slice: %w", err)
		}
	}
	group := aggregateGroup{
		Driver:       "systemd",
		Parent:       slice,
		RelativePath: slice,
		KeeperUnit:   keeper,
	}
	if _, err := os.Stat(filepath.Join(p.cfg.CgroupRoot, group.RelativePath)); err != nil {
		cleanup()
		return aggregateGroup{}, fmt.Errorf("aggregate systemd slice path unavailable: %w", err)
	}
	return group, nil
}

func (p *Provider) hostSystemd(ctx context.Context, program string, args ...string) (string, error) {
	if program != "systemctl" && program != "systemd-run" {
		return "", fmt.Errorf("unsupported host systemd helper %q", program)
	}
	hostProgram := "/usr/bin/" + program
	dockerArgs := []string{
		"run", "--rm",
		"--pull", p.cfg.PullPolicy,
		"--network", "none",
		"--mount", "type=bind,src=/,dst=/host,readonly",
		"--entrypoint", "/bin/sh",
		p.cfg.DinDImage,
		"-c", `program="$1"; shift; exec chroot /host "$program" "$@"`, "runnerhub-systemd", hostProgram,
	}
	dockerArgs = append(dockerArgs, args...)
	return p.run.Run(ctx, p.cfg.DockerBinary, dockerArgs...)
}

func formatCPUQuota(cpuMillis int64) string {
	if cpuMillis <= 0 {
		return "infinity"
	}
	return strconv.FormatFloat(float64(cpuMillis)/10.0, 'f', 3, 64) + "%"
}

func enableControllers(path string) error {
	available, err := os.ReadFile(filepath.Join(path, "cgroup.controllers"))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(available))
	hasCPU, hasMemory := false, false
	for _, field := range fields {
		switch field {
		case "cpu":
			hasCPU = true
		case "memory":
			hasMemory = true
		}
	}
	if !hasCPU || !hasMemory {
		return fmt.Errorf("cgroup %s does not expose cpu+memory controllers", path)
	}
	return os.WriteFile(filepath.Join(path, "cgroup.subtree_control"), []byte("+cpu +memory"), 0o644)
}

func writeCgroupLimit(path string, resources core.Resources) error {
	cpu := "max 100000"
	if resources.CPUMillis > 0 {
		cpu = fmt.Sprintf("%d 100000", resources.CPUMillis*100)
	}
	if err := os.WriteFile(filepath.Join(path, "cpu.max"), []byte(cpu), 0o644); err != nil {
		return err
	}
	memory := "max"
	if resources.MemoryBytes > 0 {
		memory = strconv.FormatInt(resources.MemoryBytes, 10)
	}
	if err := os.WriteFile(filepath.Join(path, "memory.max"), []byte(memory), 0o644); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(path, "memory.oom.group")); err == nil {
		if err := os.WriteFile(filepath.Join(path, "memory.oom.group"), []byte("1"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) verifyAggregateLimits(group aggregateGroup, resources core.Resources) error {
	path := filepath.Join(p.cfg.CgroupRoot, filepath.FromSlash(group.RelativePath))
	if resources.MemoryBytes > 0 {
		got, err := readIntFile(filepath.Join(path, "memory.max"))
		if err != nil {
			return err
		}
		if got > resources.MemoryBytes {
			return fmt.Errorf("aggregate memory.max=%d exceeds requested %d", got, resources.MemoryBytes)
		}
	}
	if resources.CPUMillis > 0 {
		b, err := os.ReadFile(filepath.Join(path, "cpu.max"))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(b))
		if len(fields) != 2 || fields[0] == "max" {
			return fmt.Errorf("aggregate cpu.max is not bounded: %q", strings.TrimSpace(string(b)))
		}
		quota, qErr := strconv.ParseInt(fields[0], 10, 64)
		period, pErr := strconv.ParseInt(fields[1], 10, 64)
		if qErr != nil || pErr != nil || quota <= 0 || period <= 0 {
			return fmt.Errorf("invalid aggregate cpu.max %q", strings.TrimSpace(string(b)))
		}
		effectiveMillis := quota * 1000 / period
		if effectiveMillis > resources.CPUMillis+10 {
			return fmt.Errorf("aggregate cpu.max=%dm exceeds requested %dm", effectiveMillis, resources.CPUMillis)
		}
	}
	return nil
}

func (p *Provider) prepareAggregateResourceGroup(ctx context.Context, s provider.WorkerSpec, secretDir string) (string, error) {
	if !s.AggregateResources {
		return "", nil
	}
	supported, _, driver, _ := p.aggregateResourceGroupCapabilities(ctx)
	if !supported {
		return "", fmt.Errorf("aggregate Docker resource groups requested but host verification is unavailable")
	}
	var group aggregateGroup
	var err error
	switch driver {
	case "cgroupfs":
		group, err = p.prepareCgroupFSGroup(s.WorkerID, s.Resources)
	case "systemd":
		group, err = p.prepareSystemdGroup(ctx, s.WorkerID, s.Resources)
	default:
		err = fmt.Errorf("unsupported aggregate cgroup driver %q", driver)
	}
	if err != nil {
		p.disableAggregateResourceGroups()
		return "", err
	}
	if err := p.verifyAggregateLimits(group, s.Resources); err != nil {
		p.disableAggregateResourceGroups()
		p.cleanupAggregateGroup(context.Background(), group)
		return "", err
	}
	marker, err := json.Marshal(group)
	if err != nil {
		p.cleanupAggregateGroup(context.Background(), group)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(secretDir, "aggregate-cgroup.json"), marker, 0o600); err != nil {
		p.cleanupAggregateGroup(context.Background(), group)
		return "", err
	}
	return group.Parent, nil
}

func (p *Provider) workerAggregateGroup(workerID string) (aggregateGroup, bool) {
	if strings.TrimSpace(workerID) == "" {
		return aggregateGroup{}, false
	}
	b, err := os.ReadFile(filepath.Join(p.cfg.StateDir, "workers", safeID(workerID), "aggregate-cgroup.json"))
	if err != nil {
		return aggregateGroup{}, false
	}
	var group aggregateGroup
	if err := json.Unmarshal(b, &group); err != nil {
		return aggregateGroup{}, false
	}
	if group.Driver != "cgroupfs" && group.Driver != "systemd" {
		return aggregateGroup{}, false
	}
	if strings.TrimSpace(group.Parent) == "" || strings.TrimSpace(group.RelativePath) == "" ||
		strings.Contains(group.RelativePath, "..") || filepath.IsAbs(group.RelativePath) {
		return aggregateGroup{}, false
	}
	return group, true
}

func (p *Provider) workerCgroupParent(workerID string) string {
	group, ok := p.workerAggregateGroup(workerID)
	if !ok {
		return ""
	}
	return group.Parent
}

func (p *Provider) cleanupAggregateResourceGroup(workerID string) {
	group, ok := p.workerAggregateGroup(workerID)
	if !ok {
		// The cgroupfs name is deterministic, so still clean up a partially
		// persisted create. Systemd groups always persist their marker before
		// long-lived containers are started.
		group = aggregateGroup{
			Driver:       "cgroupfs",
			Parent:       "/runnerhub/worker-" + safeID(workerID),
			RelativePath: "runnerhub/worker-" + safeID(workerID),
		}
	}
	p.cleanupAggregateGroup(context.Background(), group)
	p.aggregate.mu.Lock()
	delete(p.aggregate.cpu, workerID)
	p.aggregate.mu.Unlock()
}

func (p *Provider) cleanupAggregateGroup(ctx context.Context, group aggregateGroup) {
	switch group.Driver {
	case "systemd":
		if group.KeeperUnit != "" && strings.HasSuffix(group.KeeperUnit, ".service") && !strings.Contains(group.KeeperUnit, "/") && !strings.Contains(group.KeeperUnit, "..") {
			_, _ = p.hostSystemd(ctx, "systemctl", "stop", group.KeeperUnit)
		}
		if strings.HasSuffix(group.Parent, ".slice") && !strings.Contains(group.Parent, "/") && !strings.Contains(group.Parent, "..") {
			_, _ = p.hostSystemd(ctx, "systemctl", "stop", group.Parent)
		}
	case "cgroupfs":
		path := filepath.Join(p.cfg.CgroupRoot, filepath.FromSlash(group.RelativePath))
		if _, err := os.Stat(filepath.Join(path, "cgroup.kill")); err == nil {
			_ = os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("1"), 0o644)
		}
		removeEmptyCgroupTree(path)
	}
}

func removeEmptyCgroupTree(path string) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			removeEmptyCgroupTree(filepath.Join(path, entry.Name()))
		}
	}
	_ = os.Remove(path)
}

func (p *Provider) waitForPrivateDocker(ctx context.Context, container string) error {
	var last error
	for i := 0; i < 40; i++ {
		_, err := p.run.Run(ctx, p.cfg.DockerBinary,
			"exec", "-e", "DOCKER_HOST=unix://"+dindSocketPath, container,
			"docker", "info",
		)
		if err == nil {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("private Docker daemon did not become ready: %w", last)
}

func (p *Provider) verifyContainerCgroupParent(ctx context.Context, container, expected string) error {
	out, err := p.run.Run(ctx, p.cfg.DockerBinary, "inspect", container, "--format", "{{.HostConfig.CgroupParent}}")
	if err != nil {
		return err
	}
	got := strings.TrimSpace(out)
	if got != expected {
		return fmt.Errorf("container cgroup parent=%q, want %q", got, expected)
	}
	return nil
}

func unifiedCgroupPath(value string) string {
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		if strings.HasPrefix(line, "0::") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "0::"))
			if path == "" {
				return "/"
			}
			return path
		}
	}
	return ""
}

func (p *Provider) aggregateUsage(workerID string) (*provider.WorkerUsage, bool, error) {
	group, ok := p.workerAggregateGroup(workerID)
	if !ok {
		return nil, false, fmt.Errorf("worker %s has no aggregate cgroup marker", workerID)
	}
	path := filepath.Join(p.cfg.CgroupRoot, filepath.FromSlash(group.RelativePath))
	memory, err := readIntFile(filepath.Join(path, "memory.current"))
	if err != nil {
		return nil, false, err
	}
	peakMemory := memory
	if peak, err := readIntFile(filepath.Join(path, "memory.peak")); err == nil && peak > peakMemory {
		peakMemory = peak
	}
	usageUsec, err := readCPUUsageUsec(filepath.Join(path, "cpu.stat"))
	if err != nil {
		return nil, false, err
	}
	now := time.Now()
	cpuMillis := int64(0)
	p.aggregate.mu.Lock()
	if prev, ok := p.aggregate.cpu[workerID]; ok && usageUsec >= prev.usageUsec && now.After(prev.at) {
		elapsedUsec := now.Sub(prev.at).Microseconds()
		if elapsedUsec > 0 {
			cpuMillis = (usageUsec - prev.usageUsec) * 1000 / elapsedUsec
		}
	}
	p.aggregate.cpu[workerID] = cpuUsageSample{usageUsec: usageUsec, at: now}
	p.aggregate.mu.Unlock()

	oom := false
	if events, err := os.ReadFile(filepath.Join(path, "memory.events")); err == nil {
		for _, line := range strings.Split(string(events), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 || (fields[0] != "oom_kill" && fields[0] != "oom_group_kill") {
				continue
			}
			if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil && n > 0 {
				oom = true
			}
		}
	}
	return &provider.WorkerUsage{
		CPUMillis:           cpuMillis,
		MemoryBytes:         memory,
		EnvelopeCPUMillis:   cpuMillis,
		EnvelopeMemoryBytes: peakMemory,
		AggregateResources:  true,
	}, oom, nil
}

func readIntFile(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(b))
	if value == "max" {
		return 0, fmt.Errorf("%s is unbounded", path)
	}
	return strconv.ParseInt(value, 10, 64)
}

func readCPUUsageUsec(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			return strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("cpu.stat missing usage_usec")
}
