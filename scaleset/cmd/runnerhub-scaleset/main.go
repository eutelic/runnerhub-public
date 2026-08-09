package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"
	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
)
type config struct{RegistrationURL string;Name string;Labels []string;RunnerGroup string;PoolID string;ControlURL string;ControlToken string;MinRunners int;MaxRunners int;AppClientID string;AppInstallationID int64;AppPrivateKey string;PAT string}
func loadConfig()(config,error){c:=config{RegistrationURL:os.Getenv("RUNNERHUB_GITHUB_URL"),Name:os.Getenv("RUNNERHUB_SCALESET_NAME"),PoolID:os.Getenv("RUNNERHUB_POOL_ID"),ControlURL:strings.TrimRight(os.Getenv("RUNNERHUB_CONTROL_URL"),"/"),ControlToken:os.Getenv("RUNNERHUB_CONTROL_TOKEN"),RunnerGroup:os.Getenv("RUNNERHUB_RUNNER_GROUP"),PAT:os.Getenv("RUNNERHUB_GITHUB_TOKEN"),AppClientID:os.Getenv("RUNNERHUB_GITHUB_APP_CLIENT_ID")};c.Labels=splitCSV(os.Getenv("RUNNERHUB_SCALESET_LABELS"));c.MinRunners=intEnv("RUNNERHUB_MIN_RUNNERS",0);c.MaxRunners=intEnv("RUNNERHUB_MAX_RUNNERS",10);c.AppInstallationID=int64(intEnv("RUNNERHUB_GITHUB_APP_INSTALLATION_ID",0));if path:=os.Getenv("RUNNERHUB_GITHUB_APP_PRIVATE_KEY_FILE");path!=""{b,err:=os.ReadFile(path);if err!=nil{return c,fmt.Errorf("read github app key: %w",err)};c.AppPrivateKey=string(b)};if c.RunnerGroup==""{c.RunnerGroup=scaleset.DefaultRunnerGroup};if c.RegistrationURL==""||c.Name==""||c.PoolID==""||c.ControlURL==""{return c,errors.New("RUNNERHUB_GITHUB_URL, RUNNERHUB_SCALESET_NAME, RUNNERHUB_POOL_ID and RUNNERHUB_CONTROL_URL are required")};if c.MaxRunners<1||c.MinRunners<0||c.MinRunners>c.MaxRunners{return c,errors.New("invalid min/max runner values")};return c,nil}
func(c config)client()(*scaleset.Client,error){info:=scaleset.SystemInfo{System:"runnerhub",Subsystem:"scaleset",Version:"0.1.0",CommitSHA:"unknown"};app:=scaleset.GitHubAppAuth{ClientID:c.AppClientID,InstallationID:c.AppInstallationID,PrivateKey:c.AppPrivateKey};if app.Validate()==nil{return scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{GitHubConfigURL:c.RegistrationURL,GitHubAppAuth:app,SystemInfo:info})};if c.PAT==""{return nil,errors.New("github app credentials or RUNNERHUB_GITHUB_TOKEN are required")};return scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL:c.RegistrationURL,PersonalAccessToken:c.PAT,SystemInfo:info})}
type runnerHubClient struct{base,token,poolID string;http *http.Client};type workerResponse struct{ID string `json:"id"`;RunnerName string `json:"runnerName"`}
func(c runnerHubClient)create(ctx context.Context,name,jit string)(workerResponse,error){var out workerResponse;err:=c.do(ctx,http.MethodPost,"/api/v1/pools/"+c.poolID+"/scale-workers",map[string]string{"runnerName":name,"jitConfig":jit},&out);return out,err}
func(c runnerHubClient)event(ctx context.Context,runnerName,state string)error{return c.do(ctx,http.MethodPost,"/api/v1/scale-events",map[string]string{"poolId":c.poolID,"runnerName":runnerName,"state":state},nil)}
func(c runnerHubClient)do(ctx context.Context,method,path string,body any,out any)error{b,err:=json.Marshal(body);if err!=nil{return err};req,err:=http.NewRequestWithContext(ctx,method,c.base+path,bytes.NewReader(b));if err!=nil{return err};req.Header.Set("Content-Type","application/json");if c.token!=""{req.Header.Set("Authorization","Bearer "+c.token)};resp,err:=c.http.Do(req);if err!=nil{return err};defer resp.Body.Close();if resp.StatusCode<200||resp.StatusCode>=300{msg,_:=io.ReadAll(io.LimitReader(resp.Body,4096));return fmt.Errorf("runnerhub %s returned %d: %s",path,resp.StatusCode,strings.TrimSpace(string(msg)))};if out!=nil{return json.NewDecoder(resp.Body).Decode(out)};return nil}
type scaler struct{mu sync.Mutex;names map[string]string;min,max,scaleSetID int;github *scaleset.Client;hub runnerHubClient;logger *slog.Logger}
func(s *scaler)count()int{s.mu.Lock();defer s.mu.Unlock();return len(s.names)}
func(s *scaler)HandleDesiredRunnerCount(ctx context.Context,count int)(int,error){target:=min(s.max,s.min+count);for s.count()<target{name:=fmt.Sprintf("runnerhub-%d-%d",time.Now().UnixNano(),s.count());jit,err:=s.github.GenerateJitRunnerConfig(ctx,&scaleset.RunnerScaleSetJitRunnerSetting{Name:name},s.scaleSetID);if err!=nil{return s.count(),fmt.Errorf("generate jit: %w",err)};wr,err:=s.hub.create(ctx,name,jit.EncodedJITConfig);if err!=nil{return s.count(),err};s.mu.Lock();s.names[name]=wr.ID;s.mu.Unlock();s.logger.Info("provisioning runner","runner",name,"worker",wr.ID)};return s.count(),nil}
func(s *scaler)HandleJobStarted(ctx context.Context,info *scaleset.JobStarted)error{return s.hub.event(ctx,info.RunnerName,"busy")}
func(s *scaler)HandleJobCompleted(ctx context.Context,info *scaleset.JobCompleted)error{if err:=s.hub.event(ctx,info.RunnerName,"completed");err!=nil{return err};s.mu.Lock();delete(s.names,info.RunnerName);s.mu.Unlock();return nil}
func main(){ctx,cancel:=signal.NotifyContext(context.Background(),os.Interrupt);defer cancel();c,err:=loadConfig();if err!=nil{slog.Error("configuration","error",err);os.Exit(2)};gh,err:=c.client();if err!=nil{slog.Error("github client","error",err);os.Exit(2)};groupID:=1;if c.RunnerGroup!=scaleset.DefaultRunnerGroup{g,err:=gh.GetRunnerGroupByName(ctx,c.RunnerGroup);if err!=nil{slog.Error("runner group","error",err);os.Exit(1)};groupID=g.ID};labels:=make([]scaleset.Label,0,len(c.Labels));for _,l:=range c.Labels{labels=append(labels,scaleset.Label{Name:l})};if len(labels)==0{labels=[]scaleset.Label{{Name:c.Name}}};set,err:=gh.CreateRunnerScaleSet(ctx,&scaleset.RunnerScaleSet{Name:c.Name,RunnerGroupID:groupID,Labels:labels,RunnerSetting:scaleset.RunnerSetting{DisableUpdate:true}});if err!=nil{slog.Error("create scale set","error",err);os.Exit(1)};hostname,_:=os.Hostname();if hostname==""{hostname="runnerhub"};sessions,err:=gh.MessageSessionClient(ctx,set.ID,hostname);if err!=nil{slog.Error("message session","error",err);os.Exit(1)};defer sessions.Close(context.Background());l,err:=listener.New(sessions,listener.Config{ScaleSetID:set.ID,MaxRunners:c.MaxRunners,Logger:slog.Default()});if err!=nil{slog.Error("listener","error",err);os.Exit(1)};sc:=&scaler{names:map[string]string{},min:c.MinRunners,max:c.MaxRunners,scaleSetID:set.ID,github:gh,hub:runnerHubClient{base:c.ControlURL,token:c.ControlToken,poolID:c.PoolID,http:&http.Client{Timeout:30*time.Second}},logger:slog.Default()};if err:=l.Run(ctx,sc);err!=nil&&!errors.Is(err,context.Canceled){slog.Error("listener stopped","error",err);os.Exit(1)}}
func splitCSV(s string)[]string{var out []string;for _,v:=range strings.Split(s,","){if v=strings.TrimSpace(v);v!=""{out=append(out,v)}};return out}
func intEnv(k string,d int)int{v:=strings.TrimSpace(os.Getenv(k));if v==""{return d};n,err:=strconv.Atoi(v);if err!=nil{return d};return n}
