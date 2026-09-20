package githubrunner

import("context";"errors";"strings";"github.com/eutelic/runnerhub/internal/core")
var ErrNotFound=errors.New("github runner not found")
type JITConfigRequest struct{Scope core.GitHubScope `json:"scope"`;RunnerName string `json:"runnerName"`;RunnerGroupID int64 `json:"runnerGroupId"`;Labels []string `json:"labels"`;WorkFolder string `json:"workFolder,omitempty"`}
type JITConfig struct{RunnerID int64 `json:"runnerId"`;RunnerName string `json:"runnerName"`;EncodedConfig string `json:"-"`}
type Runner struct{ID int64 `json:"id"`;Name string `json:"name"`;OS string `json:"os,omitempty"`;Status string `json:"status"`;Busy bool `json:"busy"`;Ephemeral bool `json:"ephemeral,omitempty"`;Version string `json:"version,omitempty"`;Labels []string `json:"labels"`}
type JobStep struct{Name string `json:"name"`;Status string `json:"status"`;Conclusion string `json:"conclusion,omitempty"`;Number int `json:"number,omitempty"`}
type WorkflowJob struct{ID int64 `json:"id"`;Name string `json:"name"`;Status string `json:"status"`;Conclusion string `json:"conclusion,omitempty"`;RunnerName string `json:"runnerName,omitempty"`;Steps []JobStep `json:"steps,omitempty"`}
type WorkflowJobsBackend interface{ListWorkflowJobs(context.Context,core.GitHubScope,int64)([]WorkflowJob,error)}
type Backend interface{GenerateJITConfig(context.Context,JITConfigRequest)(JITConfig,error);GetRunner(context.Context,core.GitHubScope,int64)(Runner,error);DeleteRunner(context.Context,core.GitHubScope,int64)error}
func ValidateJITConfigRequest(req JITConfigRequest)error{if err:=core.ValidateGitHubScope(req.Scope);err!=nil{return err};if strings.TrimSpace(req.RunnerName)==""{return errors.New("runner name is required")};if req.RunnerGroupID<=0{return errors.New("runner group id must be positive")};return core.ValidateLabels(req.Labels)}
