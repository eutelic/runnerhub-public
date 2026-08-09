package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/provider"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)
type Config struct{Binary string;KernelImage string;RootFS string;StateDir string;HostID string;GuestBootArgs string;MkfsBinary string}
type Provider struct{cfg Config}
func New(c Config)(*Provider,error){if c.Binary==""{c.Binary="firecracker"};if c.StateDir==""{c.StateDir="/var/lib/runnerhub/firecracker"};if c.MkfsBinary==""{c.MkfsBinary="mkfs.ext4"};if c.KernelImage==""||c.RootFS==""{return nil,errors.New("firecracker kernel and rootfs are required")};return &Provider{cfg:c},nil}
func(p *Provider)Capabilities(ctx context.Context)(provider.Capabilities,error){if runtime.GOOS!="linux"{return provider.Capabilities{},errors.New("firecracker requires linux")};if _,err:=os.Stat("/dev/kvm");err!=nil{return provider.Capabilities{},errors.New("/dev/kvm unavailable")};if err:=exec.CommandContext(ctx,p.cfg.Binary,"--version").Run();err!=nil{return provider.Capabilities{},err};return provider.Capabilities{Provider:"firecracker",OS:"linux",Architecture:runtime.GOARCH,IsolationModes:[]core.IsolationMode{core.IsolationMicroVM},Features:map[string]bool{"kvm":true,"ephemeralRootFS":true,"dockerInsideVM":true},Labels:map[string]string{"isolation.microvm":"true"}},nil}
func(p *Provider)Validate(_ context.Context,s provider.WorkerSpec)error{if s.Isolation!=core.IsolationMicroVM{return errors.New("firecracker provider requires microvm isolation")};if s.JITConfig==""{return errors.New("jit config required")};if s.Resources.GPUs>0{return errors.New("gpu passthrough is not enabled for firecracker provider")};return nil}
func(p *Provider)Create(ctx context.Context,s provider.WorkerSpec)(provider.WorkerHandle,error){if err:=p.Validate(ctx,s);err!=nil{return provider.WorkerHandle{},err};dir:=filepath.Join(p.cfg.StateDir,safe(s.WorkerID));if err:=os.MkdirAll(dir,0700);err!=nil{return provider.WorkerHandle{},err};root:=filepath.Join(dir,"rootfs.ext4");if err:=copyFile(p.cfg.RootFS,root);err!=nil{return provider.WorkerHandle{},err};seed,err:=p.buildSeed(ctx,dir,s.JITConfig);if err!=nil{return provider.WorkerHandle{},err};mem:=s.Resources.MemoryBytes/(1024*1024);if mem<512{mem=512};vcpus:=s.Resources.CPUMillis/1000;if vcpus<1{vcpus=1};cfg:=map[string]any{"boot-source":map[string]any{"kernel_image_path":p.cfg.KernelImage,"boot_args":defaultString(p.cfg.GuestBootArgs,"console=ttyS0 reboot=k panic=1 pci=off")},"drives":[]map[string]any{{"drive_id":"rootfs","path_on_host":root,"is_root_device":true,"is_read_only":false},{"drive_id":"runnerhub-seed","path_on_host":seed,"is_root_device":false,"is_read_only":true}},"machine-config":map[string]any{"vcpu_count":vcpus,"mem_size_mib":mem}};b,_:=json.Marshal(cfg);cfgPath:=filepath.Join(dir,"vm.json");if err:=os.WriteFile(cfgPath,b,0600);err!=nil{return provider.WorkerHandle{},err};cmd:=exec.CommandContext(ctx,p.cfg.Binary,"--no-api","--config-file",cfgPath);logf,err:=os.OpenFile(filepath.Join(dir,"console.log"),os.O_CREATE|os.O_WRONLY|os.O_APPEND,0600);if err!=nil{return provider.WorkerHandle{},err};cmd.Stdout=logf;cmd.Stderr=logf;if err:=cmd.Start();err!=nil{_=logf.Close();return provider.WorkerHandle{},err};_=os.WriteFile(filepath.Join(dir,"pid"),[]byte(strconv.Itoa(cmd.Process.Pid)),0600);return provider.WorkerHandle{ProviderID:safe(s.WorkerID),HostID:p.cfg.HostID},nil}
func(p *Provider)buildSeed(ctx context.Context,dir,jitConfig string)(string,error){seedDir:=filepath.Join(dir,"seed");if err:=os.MkdirAll(seedDir,0700);err!=nil{return "",err};if err:=os.WriteFile(filepath.Join(seedDir,"jitconfig"),[]byte(jitConfig),0600);err!=nil{return "",err};if err:=os.WriteFile(filepath.Join(seedDir,"manifest"),[]byte("RUNNERHUB_SEED_V1\n"),0600);err!=nil{return "",err};seed:=filepath.Join(dir,"seed.ext4");f,err:=os.OpenFile(seed,os.O_CREATE|os.O_RDWR|os.O_TRUNC,0600);if err!=nil{return "",err};if err:=f.Truncate(16<<20);err!=nil{_=f.Close();return "",err};_=f.Close();cmd:=exec.CommandContext(ctx,p.cfg.MkfsBinary,"-q","-F","-d",seedDir,seed);if out,err:=cmd.CombinedOutput();err!=nil{return "",fmt.Errorf("build firecracker seed: %w: %s",err,strings.TrimSpace(string(out)))};return seed,nil}
func(p *Provider)Inspect(_ context.Context,h provider.WorkerHandle)(provider.WorkerState,error){pid,err:=p.pid(h);if err!=nil{if errors.Is(err,os.ErrNotExist){return provider.WorkerState{State:core.WorkerDeleted},nil};return provider.WorkerState{},err};proc,err:=os.FindProcess(pid);if err!=nil{return provider.WorkerState{},err};if err:=proc.Signal(syscall.Signal(0));err==nil{return provider.WorkerState{State:core.WorkerReady,SafeDetail:"microvm process running"},nil};return provider.WorkerState{State:core.WorkerCompleted,SafeDetail:"microvm process exited"},nil}
func(p *Provider)Stop(_ context.Context,h provider.WorkerHandle)error{pid,err:=p.pid(h);if err!=nil{return nil};proc,_:=os.FindProcess(pid);return proc.Signal(syscall.SIGTERM)}
func(p *Provider)Delete(ctx context.Context,h provider.WorkerHandle)error{_=p.Stop(ctx,h);return os.RemoveAll(filepath.Join(p.cfg.StateDir,safe(h.ProviderID)))}
func(p *Provider)pid(h provider.WorkerHandle)(int,error){b,err:=os.ReadFile(filepath.Join(p.cfg.StateDir,safe(h.ProviderID),"pid"));if err!=nil{return 0,err};return strconv.Atoi(strings.TrimSpace(string(b)))}
func copyFile(src,dst string)error{in,err:=os.Open(src);if err!=nil{return err};defer in.Close();out,err:=os.OpenFile(dst,os.O_CREATE|os.O_TRUNC|os.O_WRONLY,0600);if err!=nil{return err};_,cpErr:=io.Copy(out,in);closeErr:=out.Close();if cpErr!=nil{return cpErr};return closeErr}
func safe(s string)string{s=strings.Map(func(r rune)rune{if r>='a'&&r<='z'||r>='A'&&r<='Z'||r>='0'&&r<='9'||r=='-'{return r};return '-'},s);return strings.Trim(s,"-")}
func defaultString(a,b string)string{if a!=""{return a};return b}
