package policy

import (
	"errors"
	"fmt"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/images"
	"strings"
)
type Request struct { Pool core.RunnerPool; Repository string; Workflow string; Public bool; Privileged bool; DockerSocket bool }
type Engine struct { AllowPublic bool; AllowPrivileged bool; AllowedImages map[string]struct{}; MaxResources core.Resources; RequirePinnedImages bool }
func (e Engine) Evaluate(r Request) error { if r.Public&&!e.AllowPublic{return errors.New("public repositories are denied by policy")};if (r.Privileged||r.DockerSocket)&&!e.AllowPrivileged{return errors.New("privileged worker capabilities are denied by policy")};if len(r.Pool.AllowedRepositories)>0&&!containsFold(r.Pool.AllowedRepositories,r.Repository){return fmt.Errorf("repository %q is not allowed by pool",r.Repository)};if len(r.Pool.AllowedWorkflows)>0&&!containsFold(r.Pool.AllowedWorkflows,r.Workflow){return fmt.Errorf("workflow %q is not allowed by pool",r.Workflow)};if e.RequirePinnedImages&&r.Pool.Image!=""{if err:=images.ValidatePinned(r.Pool.Image);err!=nil{return err}};if len(e.AllowedImages)>0{if _,ok:=e.AllowedImages[r.Pool.Image];!ok{return fmt.Errorf("image %q is not allowlisted",r.Pool.Image)}};if e.MaxResources.CPUMillis>0&&r.Pool.Resources.CPUMillis>e.MaxResources.CPUMillis{return errors.New("cpu request exceeds policy")};if e.MaxResources.MemoryBytes>0&&r.Pool.Resources.MemoryBytes>e.MaxResources.MemoryBytes{return errors.New("memory request exceeds policy")};return nil }
func containsFold(xs []string,v string)bool{for _,x:=range xs{if strings.EqualFold(x,v){return true}};return false}
