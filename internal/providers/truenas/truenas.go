package truenas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/provider"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)
type Runner interface{Run(context.Context,string,...string)(string,error)};type execRunner struct{}
func(execRunner)Run(ctx context.Context,n string,a ...string)(string,error){b,err:=exec.CommandContext(ctx,n,a...).CombinedOutput();if err!=nil{return string(b),fmt.Errorf("%s failed: %w: %s",n,err,strings.TrimSpace(string(b)))};return string(b),nil}
type Config struct{Midclt string;HostID string;Root string;AllowedImages []string};type Provider struct{cfg Config;run Runner;allowed map[string]struct{}}
func New(c Config)(*Provider,error){return newWithRunner(c,execRunner{})}
func newWithRunner(c Config,r Runner)(*Provider,error){if c.Midclt==""{c.Midclt="midclt"};if c.Root==""{c.Root="/mnt/Databases/containers/runnerhub"};if !filepath.IsAbs(c.Root){return nil,errors.New("truenas root must be absolute")};if len(c.AllowedImages)==0{c.AllowedImages=[]string{"ghcr.io/actions/actions-runner:latest"}};a:=map[string]struct{}{};for _,x:=range c.AllowedImages{a[x]=struct{}{}};return &Provider{cfg:c,run:r,allowed:a},nil}
func(p *Provider)Capabilities(ctx context.Context)(provider.Capabilities,error){if _,err:=p.run.Run(ctx,p.cfg.Midclt,"call","system.version");err!=nil{return provider.Capabilities{},err};return provider.Capabilities{Provider:"truenas",OS:"linux",Architecture:"amd64",IsolationModes:[]core.IsolationMode{core.IsolationContainerTrusted},Features:map[string]bool{"customApps":true,"structuredCompose":true},Labels:map[string]string{"appliance":"truenas","isolation.container-trusted":"true"}},nil}
func(p *Provider)Validate(_ context.Context,s provider.WorkerSpec)error{if s.Isolation!=core.IsolationContainerTrusted{return errors.New("truenas custom-app provider currently supports container-trusted only")};if _,ok:=p.allowed[s.Image];!ok{return fmt.Errorf("image %q is not allowlisted",s.Image)};if s.JITConfig==""{return errors.New("jit config required")};return nil}
func(p *Provider)Create(ctx context.Context,s provider.WorkerSpec)(provider.WorkerHandle,error){if s.Image==""{s.Image="ghcr.io/actions/actions-runner:latest"};if err:=p.Validate(ctx,s);err!=nil{return provider.WorkerHandle{},err};app:=appName(s.WorkerID);root:=filepath.Join(p.cfg.Root,"workers",app);if !strings.HasPrefix(filepath.Clean(root),filepath.Clean(p.cfg.Root)+string(filepath.Separator)){return provider.WorkerHandle{},errors.New("generated path escaped truenas root")};if err:=os.MkdirAll(root,0700);err!=nil{return provider.WorkerHandle{},err};jitPath:=filepath.Join(root,"jitconfig");if err:=os.WriteFile(jitPath,[]byte(s.JITConfig),0600);err!=nil{return provider.WorkerHandle{},err};compose:=map[string]any{"services":map[string]any{"runner":map[string]any{"image":s.Image,"restart":"no","command":[]string{"/bin/bash","-lc","exec /home/runner/run.sh --jitconfig \"$(cat /run/runnerhub/jitconfig)\""},"volumes":[]string{jitPath+":/run/runnerhub/jitconfig:ro"},"tmpfs":[]string{"/home/runner/_work:rw,exec,nosuid,nodev,size="+strconv.FormatInt(workSize(s),10)},"labels":map[string]string{"io.runnerhub.worker":s.WorkerID,"io.runnerhub.pool":s.PoolID},"security_opt":[]string{"no-new-privileges:true"}}}};payload:=map[string]any{"app_name":app,"custom_app":true,"custom_compose_config":compose};b,_:=json.Marshal(payload);if _,err:=p.run.Run(ctx,p.cfg.Midclt,"call","-job","app.create",string(b));err!=nil{return provider.WorkerHandle{},err};return provider.WorkerHandle{ProviderID:app,HostID:p.cfg.HostID},nil}
func(p *Provider)Inspect(ctx context.Context,h provider.WorkerHandle)(provider.WorkerState,error){out,err:=p.run.Run(ctx,p.cfg.Midclt,"call","app.get_instance",h.ProviderID,"{}");if err!=nil{if strings.Contains(strings.ToLower(err.Error()),"not found"){return provider.WorkerState{State:core.WorkerDeleted},nil};return provider.WorkerState{},err};var v struct{State string `json:"state"`};if err:=json.Unmarshal([]byte(out),&v);err!=nil{return provider.WorkerState{},err};switch v.State{case "RUNNING","DEPLOYING":return provider.WorkerState{State:core.WorkerReady,SafeDetail:"truenas app "+strings.ToLower(v.State)},nil;case "STOPPED":return provider.WorkerState{State:core.WorkerCompleted,SafeDetail:"truenas app stopped"},nil;case "CRASHED":return provider.WorkerState{State:core.WorkerFailed,SafeDetail:"truenas app crashed"},nil};return provider.WorkerState{State:core.WorkerStarting,SafeDetail:"truenas app "+v.State},nil}
func(p *Provider)Stop(ctx context.Context,h provider.WorkerHandle)error{_,err:=p.run.Run(ctx,p.cfg.Midclt,"call","-job","app.stop",h.ProviderID);return err}
func(p *Provider)Delete(ctx context.Context,h provider.WorkerHandle)error{_,err:=p.run.Run(ctx,p.cfg.Midclt,"call","-job","app.delete",h.ProviderID);if err!=nil&&!strings.Contains(strings.ToLower(err.Error()),"not found"){return err};_=os.RemoveAll(filepath.Join(p.cfg.Root,"workers",h.ProviderID));return nil}
var nonName=regexp.MustCompile(`[^a-z0-9-]+`)
func appName(id string)string{s:=strings.ToLower(id);s=nonName.ReplaceAllString(s,"-");s=strings.Trim(s,"-");if s==""{s="worker"};s="rh-"+s;if len(s)>40{s=s[:40]};return strings.Trim(s,"-")}
func workSize(s provider.WorkerSpec)int64{if s.Resources.DiskBytes>0{return s.Resources.DiskBytes};return 20<<30}
