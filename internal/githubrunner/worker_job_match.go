package githubrunner

import (
	"strconv"
	"strings"
)

// MatchAssignedWorkflowJob matches a job to a RunnerHub worker using GitHub's
// assigned runner identity, with optional additional exact job ID/name checks.
// A job title alone is never sufficient: matrix jobs can reuse the same name,
// and a runner may be reassigned across different workflow runs.
func MatchAssignedWorkflowJob(runnerName, jobName, jobID string, jobs []WorkflowJob) (WorkflowJob,bool) {
	runnerName=strings.TrimSpace(runnerName)
	if runnerName=="" { return WorkflowJob{},false }
	numericID,parseErr:=strconv.ParseInt(strings.TrimSpace(jobID),10,64)
	if strings.TrimSpace(jobID)!=""&&(parseErr!=nil||numericID<=0) {
		return WorkflowJob{},false
	}
	var matched WorkflowJob
	found:=false
	for _,job:=range jobs {
		if job.RunnerName!=runnerName { continue }
		if numericID>0&&job.ID!=numericID{ continue }
		if strings.TrimSpace(jobName)!=""&&job.Name!=jobName{ continue }
		if found { return WorkflowJob{},false }
		matched=job
		found=true
	}
	return matched,found
}

// IsTerminalJobConclusion accepts only GitHub's recognized definitive job
// outcomes. Unrecognized strings cannot trigger automatic worker cleanup.
func IsTerminalJobConclusion(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)){
	case "success","failure","cancelled","timed_out":
		return true
	default:
		return false
	}
}
