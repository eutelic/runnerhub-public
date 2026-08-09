package githubrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/example-user/runnerhub/internal/core"
	"net/http"
	"net/url"
	"strings"
)

type Repository struct { ID int64 `json:"id"`; FullName string `json:"fullName"`; Private bool `json:"private"`; DefaultBranch string `json:"defaultBranch"` }
type RunnerGroup struct { ID int64 `json:"id"`; Name string `json:"name"`; Visibility string `json:"visibility"` }
type Catalog interface { ListRepositories(context.Context,core.GitHubScope)([]Repository,error); ListRunnerGroups(context.Context,string)([]RunnerGroup,error) }
type HTTPCatalog struct { client *http.Client; tokens TokenSource; base *url.URL; apiVersion string }
func NewHTTPCatalog(client *http.Client,tokens TokenSource,base string)(*HTTPCatalog,error){ if tokens==nil{return nil,errors.New("token source required")}; if client==nil{client=http.DefaultClient}; if base==""{base="https://api.github.com"}; u,err:=url.Parse(base); if err!=nil{return nil,err}; return &HTTPCatalog{client:client,tokens:tokens,base:u,apiVersion:"2026-03-10"},nil }
func (c *HTTPCatalog) ListRepositories(ctx context.Context,scope core.GitHubScope)([]Repository,error){ token,err:=c.tokens.Token(ctx,scope); if err!=nil{return nil,err}; var response struct{Repositories []struct{ID int64 `json:"id"`; FullName string `json:"full_name"`; Private bool `json:"private"`; DefaultBranch string `json:"default_branch"`} `json:"repositories"`}; if err:=c.get(ctx,"/installation/repositories?per_page=100",token,&response); err!=nil{return nil,err}; out:=make([]Repository,0,len(response.Repositories)); for _,r:=range response.Repositories{out=append(out,Repository{ID:r.ID,FullName:r.FullName,Private:r.Private,DefaultBranch:r.DefaultBranch})}; return out,nil }
func (c *HTTPCatalog) ListRunnerGroups(ctx context.Context,org string)([]RunnerGroup,error){ scope:=core.GitHubScope{Kind:core.ScopeOrganization,Owner:org}; token,err:=c.tokens.Token(ctx,scope); if err!=nil{return nil,err}; var response struct{RunnerGroups []RunnerGroup `json:"runner_groups"`}; if err:=c.get(ctx,"/orgs/"+url.PathEscape(org)+"/actions/runner-groups?per_page=100",token,&response); err!=nil{return nil,err}; return response.RunnerGroups,nil }
func (c *HTTPCatalog) get(ctx context.Context,path,token string,out any) error { rel,_:=url.Parse(path); req,err:=http.NewRequestWithContext(ctx,http.MethodGet,c.base.ResolveReference(rel).String(),nil); if err!=nil{return err}; req.Header.Set("Authorization","Bearer "+token); req.Header.Set("Accept","application/vnd.github+json"); req.Header.Set("X-GitHub-Api-Version",c.apiVersion); resp,err:=c.client.Do(req); if err!=nil{return err}; defer resp.Body.Close(); if resp.StatusCode<200||resp.StatusCode>=300{return fmt.Errorf("github catalog request returned %d",resp.StatusCode)}; return json.NewDecoder(resp.Body).Decode(out) }
var _ = strings.TrimSpace
