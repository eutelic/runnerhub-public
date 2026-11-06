package deployer

import (
 "context"
 "errors"
 "fmt"
 "strings"

 "github.com/gorilla/websocket"
)

// TrueNASImagePreflight proves that an immutable custom Compose update is
// authorized before stopping CI admissions. This is a read-only capability
// check; actual app.update can still fail if permissions change later.
type TrueNASImagePreflight interface {
 VerifyImmutableImageUpdate(context.Context,string,string) error
}

func (c *TrueNASClient) VerifyImmutableImageUpdate(ctx context.Context,app,image string)error{
 if !appNamePattern.MatchString(app)||len(app)>40 {
  return errors.New("invalid TrueNAS app name")
 }
 if !imageDigestPattern.MatchString(strings.TrimPrefix(image,"ghcr.io/eutelic/runnerhub-control@"))||
  !strings.HasPrefix(image,"ghcr.io/eutelic/runnerhub-control@"){
  return errors.New("immutable control image digest required")
 }
 conn,_,err:=c.dialer.DialContext(ctx,c.endpoint,nil)
 if err!=nil{return fmt.Errorf("connect to TrueNAS for capability check: %w",err)}
 defer conn.Close()
 if err:=preflightAuthenticate(ctx,c,conn);err!=nil{return err}
 var me struct {Roles []string `json:"roles"`}
 if err:=c.call(ctx,conn,"auth.me",[]any{},&me);err!=nil{
  return fmt.Errorf("query TrueNAS service account roles: %w",err)
 }
 admin:=false
 for _,role:=range me.Roles{if role=="FULL_ADMIN"{admin=true}}
 if !admin{return errors.New("TrueNAS API key lacks FULL_ADMIN required to update custom Compose; CI admission was not paused")}
 var installed pinnedRunnerHubApp
 if err:=c.call(ctx,conn,"app.get_instance",[]any{app,map[string]any{
  "extra":map[string]any{"retrieve_config":true},
 }},&installed);err!=nil{return fmt.Errorf("inspect TrueNAS custom app: %w",err)}
 if (installed.Name!=app&&installed.ID!=app)||!installed.CustomApp||installed.State!="RUNNING"{
  return errors.New("TrueNAS RunnerHub app is not a running custom Compose application")
 }
 if _,err:=pinRunnerHubComposeConfig(installed.Config,image);err!=nil{
  return fmt.Errorf("validate pinned control image mapping: %w",err)
 }
 return nil
}

func preflightAuthenticate(ctx context.Context,c *TrueNASClient,conn *websocket.Conn)error{
 var auth struct {ResponseType string `json:"response_type"`}
 if err:=c.call(ctx,conn,"auth.login_ex",[]any{map[string]any{
  "mechanism":"API_KEY_PLAIN","username":c.username,"api_key":c.apiKey,
  "login_options":map[string]any{"user_info":false},
 }},&auth);err!=nil{return fmt.Errorf("authenticate to TrueNAS: %w",err)}
 if auth.ResponseType!="SUCCESS"{return errors.New("TrueNAS authentication failed")}
 return nil
}
