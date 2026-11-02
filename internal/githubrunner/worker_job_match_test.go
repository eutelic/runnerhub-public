package githubrunner

import "testing"

func TestMatchAssignedWorkflowJobRequiresExactRunnerAndUniqueJob(t *testing.T){
	jobs:=[]WorkflowJob{
		{ID:1,RunnerName:"runner-a",Name:"publish (agent, runnerhub-agent)",Status:"completed",Conclusion:"failure"},
		{ID:2,RunnerName:"runner-b",Name:"publish (agent, runnerhub-agent)",Status:"completed",Conclusion:"success"},
	}
	if job,ok:=MatchAssignedWorkflowJob("runner-a","publish (agent, runnerhub-agent)","",jobs);!ok||job.ID!=1{
		t.Fatalf("exact runner not matched: %+v %v",job,ok)
	}
	if _,ok:=MatchAssignedWorkflowJob("runner-a","publish (agent, runnerhub-agent)","2",jobs);ok{
		t.Fatal("different GitHub job ID bypassed runner check")
	}
	if _,ok:=MatchAssignedWorkflowJob("runner-b","publish (agent, runnerhub-agent)","1",jobs);ok{
		t.Fatal("job ID bypassed runner identity")
	}
	if _,ok:=MatchAssignedWorkflowJob("","publish (agent, runnerhub-agent)","",jobs);ok{
		t.Fatal("name-only match accepted")
	}
	if _,ok:=MatchAssignedWorkflowJob("runner-a","different name","",jobs);ok{
		t.Fatal("different job name accepted")
	}
	if _,ok:=MatchAssignedWorkflowJob("runner-a","publish (agent, runnerhub-agent)","invalid",jobs);ok{
		t.Fatal("invalid numeric ID accepted")
	}
	ambiguous:=append(jobs,WorkflowJob{ID:3,RunnerName:"runner-a",Name:"publish (agent, runnerhub-agent)"})
	if _,ok:=MatchAssignedWorkflowJob("runner-a","publish (agent, runnerhub-agent)","",ambiguous);ok{
		t.Fatal("ambiguous reused runner accepted")
	}
	if job,ok:=MatchAssignedWorkflowJob("runner-a","publish (agent, runnerhub-agent)","1",ambiguous);!ok||job.ID!=1{
		t.Fatalf("unique exact job ID not matched: %+v %v",job,ok)
	}
}

func TestIsTerminalJobConclusion(t *testing.T){
	for _,value:=range []string{"success","failure","cancelled","timed_out"}{
		if !IsTerminalJobConclusion(value){t.Fatalf("expected terminal: %q",value)}
	}
	for _,value:=range []string{"","in_progress","skipped","waiting","unknown","dangerous\"secret"}{
		if IsTerminalJobConclusion(value){t.Fatalf("unexpected terminal: %q",value)}
	}
}
