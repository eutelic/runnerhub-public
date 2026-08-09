package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/provider"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)
type Config struct{BaseURL string;TokenID string;TokenSecret string;Node string;TemplateVMID int;HostID string;HTTPClient *http.Client;SnippetDir string;SnippetStore string}
type Provider struct{cfg Config;client *http.Client}
func New(c Config)(*Provider,error){if c.BaseURL==""||c.TokenID==""||c.TokenSecret==""||c.Node==""||c.TemplateVMID<=0{return nil,errors.New("proxmox base url, token, node and template vmid required")};h:=c.HTTPClient;if h==nil{h=http.DefaultClient};if c.SnippetStore==""{c.SnippetStore="local"};return &Provider{cfg:c,client:h},nil}
func(p *Provider)Capabilities(context.Context)(provider.Capabilities,error){return provider.Capabilities{Provider:"proxmox",OS:"linux",Architecture:"amd64",IsolationModes:[]core.IsolationMode{core.IsolationVMProvider},Features:map[string]bool{"clone":true,"cloudInit":true,"ephemeralDisk":true},Labels:map[string]string{"isolation.vm-provider":"true"}},nil}
func(p *Provider)Validate(_ context.Context,s provider.WorkerSpec)error{if s.Isolation!=core.IsolationVMProvider{return errors.New("proxmox provider requires vm-provider isolation")};if s.JITConfig==""{return errors.New("jit config required")};return nil}
func(p *Provider)Create(ctx context.Context,s provider.WorkerSpec)(provider.WorkerHandle,error){if err:=p.Validate(ctx,s);err!=nil{return provider.WorkerHandle{},err};vmid:=100000+hashID(s.WorkerID)%800000;form:=url.Values{"newid":{strconv.Itoa(vmid)},"name":{"runnerhub-"+s.WorkerID},"full":{"1"}};if err:=p.form(ctx,http.MethodPost,fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/clone",p.cfg.Node,p.cfg.TemplateVMID),form,nil);err!=nil{return provider.WorkerHandle{},err};if p.cfg.SnippetDir==""{return provider.WorkerHandle{},errors.New("proxmox snippet directory is required for JIT bootstrap")};snippetName:="runnerhub-"+safeName(s.WorkerID)+".yaml";if err:=p.writeSnippet(snippetName,s.JITConfig);err!=nil{return provider.WorkerHandle{},err};ci:=url.Values{"ciuser":{"runner"},"description":{"RunnerHub ephemeral worker"},"cicustom":{"user="+p.cfg.SnippetStore+":snippets/"+snippetName}};if s.Resources.CPUMillis>0{ci.Set("cores",strconv.Itoa(max(1,int((s.Resources.CPUMillis+999)/1000))))};if s.Resources.MemoryBytes>0{ci.Set("memory",strconv.FormatInt(max(512,s.Resources.MemoryBytes/(1024*1024)),10))};if err:=p.form(ctx,http.MethodPut,fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config",p.cfg.Node,vmid),ci,nil);err!=nil{return provider.WorkerHandle{},err};if err:=p.form(ctx,http.MethodPost,fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/start",p.cfg.Node,vmid),nil,nil);err!=nil{return provider.WorkerHandle{},err};return provider.WorkerHandle{ProviderID:strconv.Itoa(vmid)+"@"+safeName(s.WorkerID),HostID:p.cfg.HostID},nil}
func(p *Provider)writeSnippet(name,jit string)error{if err:=os.MkdirAll(p.cfg.SnippetDir,0700);err!=nil{return err};path:=filepath.Join(p.cfg.SnippetDir,name);if filepath.Dir(path)!=filepath.Clean(p.cfg.SnippetDir){return errors.New("invalid proxmox snippet path")};encoded:=jsonString(jit);content:="#cloud-config\nwrite_files:\n  - path: /run/runnerhub-jit\n    permissions: '0600'\n    content: "+encoded+"\nruncmd:\n  - [ sh, -lc, 'cd /home/runner/actions-runner && ./run.sh --jitconfig \"$(cat /run/runnerhub-jit)\"' ]\n";return os.WriteFile(path,[]byte(content),0600)}
func jsonString(v string)string{b,_:=json.Marshal(v);return string(b)}
func safeName(v string)string{var b strings.Builder;for _,r:=range v{if r>='a'&&r<='z'||r>='A'&&r<='Z'||r>='0'&&r<='9'||r=='-'{b.WriteRune(r)}else{b.WriteByte('-')}};return strings.Trim(b.String(),"-")}
func(p *Provider)Inspect(ctx context.Context,h provider.WorkerHandle)(provider.WorkerState,error){var out struct{Data struct{Status string `json:"status"`} `json:"data"`};if err:=p.form(ctx,http.MethodGet,fmt.Sprintf("/api2/json/nodes/%s/qemu/%s/status/current",p.cfg.Node,vmID(h.ProviderID)),nil,&out);err!=nil{return provider.WorkerState{},err};if out.Data.Status=="running"{return provider.WorkerState{State:core.WorkerReady,SafeDetail:"proxmox vm running"},nil};return provider.WorkerState{State:core.WorkerCompleted,SafeDetail:"proxmox vm "+out.Data.Status},nil}
func(p *Provider)Stop(ctx context.Context,h provider.WorkerHandle)error{return p.form(ctx,http.MethodPost,fmt.Sprintf("/api2/json/nodes/%s/qemu/%s/status/stop",p.cfg.Node,vmID(h.ProviderID)),nil,nil)}
func(p *Provider)Delete(ctx context.Context,h provider.WorkerHandle)error{_=p.Stop(ctx,h);err:=p.form(ctx,http.MethodDelete,fmt.Sprintf("/api2/json/nodes/%s/qemu/%s",p.cfg.Node,vmID(h.ProviderID)),nil,nil);if p.cfg.SnippetDir!=""{_=os.Remove(filepath.Join(p.cfg.SnippetDir,"runnerhub-"+workerKey(h.ProviderID)+".yaml"))};return err}
func(p *Provider)form(ctx context.Context,method,path string,form url.Values,out any)error{var body io.Reader;if form!=nil{body=bytes.NewBufferString(form.Encode())};req,err:=http.NewRequestWithContext(ctx,method,strings.TrimRight(p.cfg.BaseURL,"/")+path,body);if err!=nil{return err};req.Header.Set("Authorization","PVEAPIToken="+p.cfg.TokenID+"="+p.cfg.TokenSecret);if form!=nil{req.Header.Set("Content-Type","application/x-www-form-urlencoded")};resp,err:=p.client.Do(req);if err!=nil{return err};defer resp.Body.Close();if resp.StatusCode<200||resp.StatusCode>=300{b,_:=io.ReadAll(io.LimitReader(resp.Body,1024));return fmt.Errorf("proxmox returned %d: %s",resp.StatusCode,strings.TrimSpace(string(b)))};if out!=nil{return json.NewDecoder(resp.Body).Decode(out)};return nil}
func vmID(v string)string{if i:=strings.IndexByte(v,'@');i>=0{return v[:i]};return v}
func workerKey(v string)string{if i:=strings.IndexByte(v,'@');i>=0{return safeName(v[i+1:])};return safeName(v)}
func hashID(s string)int{h:=0;for _,r:=range s{h=(h*31+int(r))&0x7fffffff};return h}
