package process

import("context";"errors";"os";"os/exec";"path/filepath";"runtime";"strconv";"strings";"syscall";"time";"github.com/eutelic/runnerhub/internal/core";"github.com/eutelic/runnerhub/internal/hostinfo";"github.com/eutelic/runnerhub/internal/provider")
type Config struct{RunnerDir,StateDir,HostID string;AndroidEmulatorOnly bool};type Provider struct{cfg Config;kvmReady func()bool;availableResources func()core.Resources}
func New(c Config)(*Provider,error){if c.RunnerDir==""{return nil,errors.New("runner directory required")};if c.StateDir==""{c.StateDir="/var/lib/runnerhub/process"};return &Provider{cfg:c,kvmReady:hostinfo.KVMUsable,availableResources:func()core.Resources{return hostinfo.AvailableCapacity(c.StateDir)}},nil}
func (p *Provider) Capabilities(context.Context) (provider.Capabilities, error) {
  features := map[string]bool{"specialHardware": true}
  labels := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "isolation.host-persistent": "true"}
  // Never trust an operator-defined tag as proof of KVM: this label is
  // derived from a real ioctl by the same host account running the worker.
  if p.cfg.AndroidEmulatorOnly {
    labels[core.AndroidEmulatorDedicatedTag]="true"
    features["android-emulator-only"]=true
  }
  if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" && p.kvmReady() {
    features["kvm"] = true
    labels[core.AndroidEmulatorCapabilityTag] = "true"
  }
  return provider.Capabilities{
    Provider: "process", OS: runtime.GOOS, Architecture: runtime.GOARCH,
    Capacity: hostinfo.Capacity(p.cfg.StateDir),
    IsolationModes: []core.IsolationMode{core.IsolationHostPersistent},
    Features: features, Labels: labels,
  }, nil
}
func(p *Provider)Validate(_ context.Context,s provider.WorkerSpec)error{if strings.TrimSpace(s.WorkerID)==""||strings.TrimSpace(s.RunnerName)==""{return errors.New("worker id and runner name are required")};if s.Isolation!=core.IsolationHostPersistent{return errors.New("process provider requires host-persistent isolation")};if s.JITConfig==""{return errors.New("jit config required")}
  if p.cfg.AndroidEmulatorOnly && !core.IsAndroidEmulatorPool(core.RunnerPool{Labels:s.Labels}) {
    return errors.New("dedicated Android KVM host rejects non-emulator native jobs")
  }
  if core.IsAndroidEmulatorPool(core.RunnerPool{Labels:s.Labels}) {
    if runtime.GOOS!="linux" || runtime.GOARCH!="amd64" || !p.kvmReady() {
      return errors.New("Android emulator worker rejected: Linux amd64 KVM device unavailable to runner account at job creation")
    }
    available:=p.availableResources()
    if !core.Fits(available,core.Resources{},s.Resources) {
      return errors.New("Android emulator worker rejected: host CPU/RAM/disk capacity no longer meets reservation")
    }
    if s.Resources.CPUMillis<4000 || s.Resources.MemoryBytes<8<<30 || s.Resources.DiskBytes<20<<30 {
      return errors.New("Android emulator worker rejected: reservation requires 4 CPU, 8 GiB RAM and 20 GiB disk")
    }
  }
  return nil
}
func(p *Provider)Create(ctx context.Context,s provider.WorkerSpec)(provider.WorkerHandle,error){if err:=p.Validate(ctx,s);err!=nil{return provider.WorkerHandle{},err};h:=provider.WorkerHandle{ProviderID:safe(s.WorkerID),HostID:p.cfg.HostID};if h.ProviderID==""{return provider.WorkerHandle{},errors.New("invalid worker id")};if st,err:=p.Inspect(ctx,h);err==nil&&st.State!=core.WorkerDeleted&&st.State!=core.WorkerCompleted&&st.State!=core.WorkerFailed{return h,nil};dir:=filepath.Join(p.cfg.StateDir,h.ProviderID)
  // Every JIT worker runs in a new private copy of the trusted runner
  // installation. GitHub's ephemeral registration does not sanitize the
  // executable directory or workspaces of a previous self-hosted job.
  if err:=os.RemoveAll(dir);err!=nil{return provider.WorkerHandle{},err}
  if err:=os.MkdirAll(dir,0700);err!=nil{return provider.WorkerHandle{},err}
  workerRunnerDir:=filepath.Join(dir,"runner")
  if err:=stageNativeRunner(p.cfg.RunnerDir,workerRunnerDir);err!=nil{
    _=os.RemoveAll(dir)
    return provider.WorkerHandle{},err
  }
  // An ephemeral worker ID may be retried after an earlier completed process.
  // Do not mistake its saved exit result for the new process's status.
  exitPath:=filepath.Join(dir,"exit-code")
  if err:=os.Remove(exitPath);err!=nil&&!os.IsNotExist(err){return provider.WorkerHandle{},err}
  script:="run.sh";if runtime.GOOS=="windows"{script="run.cmd"};cmd:=exec.CommandContext(ctx,filepath.Join(workerRunnerDir,script));cmd.Dir=workerRunnerDir;cmd.Env=append(os.Environ(),"ACTIONS_RUNNER_INPUT_JITCONFIG="+s.JITConfig,"RUNNERHUB_WORKER_ID="+s.WorkerID);logf,err:=os.OpenFile(filepath.Join(dir,"runner.log"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if err!=nil{return provider.WorkerHandle{},err};cmd.Stdout=logf;cmd.Stderr=logf;if err:=cmd.Start();err!=nil{_=logf.Close();return provider.WorkerHandle{},err};if err:=os.WriteFile(filepath.Join(dir,"pid"),[]byte(strconv.Itoa(cmd.Process.Pid)),0600);err!=nil{
    _=cmd.Process.Kill()
    _=cmd.Wait() // reap a failed-to-record runner immediately
    _=logf.Close()
    return provider.WorkerHandle{},err
  }
  // Without Wait, a completed child can remain a zombie. On Linux kill(pid,0)
  // succeeds for zombies and Inspect would incorrectly report Ready forever.
  // The exit status survives subsequent process-provider inspections.
  go func() {
    err:=cmd.Wait()
    code:=0
    if err!=nil {
      code=-1
      var exitErr *exec.ExitError
      if errors.As(err,&exitErr) {code=exitErr.ExitCode()}
    }
    _ = writeNativeExitCode(dir,code)
    _=logf.Close()
  }()
  return h,nil}
func(p *Provider)Inspect(_ context.Context,h provider.WorkerHandle)(provider.WorkerState,error){if safe(h.ProviderID)==""{return provider.WorkerState{},errors.New("process handle required")};dir:=filepath.Join(p.cfg.StateDir,safe(h.ProviderID))
  if b,err:=os.ReadFile(filepath.Join(dir,"exit-code"));err==nil {
    code,parseErr:=strconv.Atoi(strings.TrimSpace(string(b)))
    if parseErr!=nil{return provider.WorkerState{},parseErr}
    if code==0 {return provider.WorkerState{State:core.WorkerCompleted,ExitCode:&code,SafeDetail:"native runner exited successfully"},nil}
    return provider.WorkerState{State:core.WorkerFailed,ExitCode:&code,SafeDetail:"native runner process exited unsuccessfully"},nil
  }else if !os.IsNotExist(err){return provider.WorkerState{},err}
  pid,err:=p.pid(h);if err!=nil{if os.IsNotExist(err){return provider.WorkerState{State:core.WorkerDeleted},nil};return provider.WorkerState{},err};proc,err:=os.FindProcess(pid);if err!=nil{return provider.WorkerState{},err};if proc.Signal(syscall.Signal(0))==nil{return provider.WorkerState{State:core.WorkerReady,SafeDetail:"runner process active"},nil};// No persisted exit code means the agent might have restarted or crashed.
  // Never report such an unknown exit as a successful job.
  return provider.WorkerState{State:core.WorkerFailed,SafeDetail:"native runner disappeared without a confirmed exit code"},nil}
func(p *Provider)Stop(_ context.Context,h provider.WorkerHandle)error{pid,err:=p.pid(h);if err!=nil{return nil};proc,_:=os.FindProcess(pid);return proc.Signal(os.Interrupt)}
func (p *Provider) Delete(ctx context.Context, h provider.WorkerHandle) error {
  key:=safe(h.ProviderID)
  if key=="" {return errors.New("process handle required")}
  state,err:=p.Inspect(ctx,h)
  if err!=nil{return err}
  if state.State==core.WorkerReady {
    stopErr:=p.Stop(ctx,h)
    if stopErr!=nil {
      next,readErr:=p.Inspect(ctx,h)
      if readErr!=nil{return readErr}
      if next.State==core.WorkerReady{return stopErr}
    }
    // Do not discard the PID/diagnostics while the native process could still
    // run. That would strand a live emulator outside RunnerHub reconciliation.
    timeout:=time.NewTimer(2*time.Second)
    defer timeout.Stop()
    ticker:=time.NewTicker(50*time.Millisecond)
    defer ticker.Stop()
    for {
      state,err=p.Inspect(ctx,h)
      if err!=nil{return err}
      if state.State!=core.WorkerReady{break}
      select{
      case <-ctx.Done():return ctx.Err()
      case <-timeout.C:
        return errors.New("native runner still running after interrupt; retained provider handle for safe retry")
      case <-ticker.C:
      }
    }
  }
  return os.RemoveAll(filepath.Join(p.cfg.StateDir,key))
}
// Write atomically so Inspect never parses a partially written exit code.
func writeNativeExitCode(dir string, code int) error {
  f,err:=os.CreateTemp(dir,".runnerhub-exit-*")
  if err!=nil{return err}
  defer os.Remove(f.Name())
  if err:=f.Chmod(0600);err!=nil{_=f.Close();return err}
  if _,err:=f.WriteString(strconv.Itoa(code));err!=nil{_=f.Close();return err}
  if err:=f.Close();err!=nil{return err}
  return os.Rename(f.Name(),filepath.Join(dir,"exit-code"))
}
func(p *Provider)pid(h provider.WorkerHandle)(int,error){key:=safe(h.ProviderID);if key==""{return 0,errors.New("process handle required")};b,err:=os.ReadFile(filepath.Join(p.cfg.StateDir,key,"pid"));if err!=nil{return 0,err};return strconv.Atoi(strings.TrimSpace(string(b)))}
func safe(v string)string{v=strings.Map(func(r rune)rune{if r>='a'&&r<='z'||r>='A'&&r<='Z'||r>='0'&&r<='9'||r=='-'{return r};return '-'},v);return strings.Trim(v,"-")}
