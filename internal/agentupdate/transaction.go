// Package agentupdate implements the safety-critical lifecycle for replacing
// exactly one enrolled Docker agent without changing its host identity.
// The engine is independent of HTTP and Docker CLI: a detached host-side
// adapter must implement the privileged operations.
package agentupdate

import (
    "context"
    "errors"
    "fmt"
    "regexp"
    "strings"
    "time"
)

const AgentImage = "ghcr.io/eutelic/runnerhub-agent"

var (
    imagePattern = regexp.MustCompile(`^ghcr\.io/eutelic/runnerhub-agent@sha256:[a-f0-9]{64}$`)
    revisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
    ErrUnsafe = errors.New("agent update preflight failed")
    ErrReview = errors.New("agent update requires operator review")
)

// Target must come from an approved publication, never arbitrary user input.
type Target struct {
    Image string `json:"image"`
    Revision string `json:"revision"`
}

func (t Target) Validate() error {
    if !imagePattern.MatchString(t.Image) || !revisionPattern.MatchString(t.Revision) {
        return fmt.Errorf("%w: immutable official agent image and 40-character revision required",ErrUnsafe)
    }
    return nil
}

// All sensitive Docker environment/mount configuration remains private to
// the host adapter and must never be exposed in audit or web responses.
type Snapshot struct {
    HostID string
    ContainerID string
    Image string
    Running bool
    SafeTopology bool
}

type Stage struct { ContainerID string }

// Adapter runs in a separate trusted host-side process, not in the agent
// container about to be stopped. Pull and Stage never stop the old agent.
// Replace retains a recoverable old container. Restore must be idempotent;
// neither restore nor cleanup may prune or target unrelated containers.
type Adapter interface {
    Capture(context.Context,string)(Snapshot,error)
    PullAndVerify(context.Context,Target)error
    StageReplacement(context.Context,Snapshot,Target)(Stage,error)
    Discard(context.Context,Snapshot,Stage)error
    Replace(context.Context,Snapshot,Stage)error
    VerifyRunning(context.Context,Stage,Target)error
    WaitForHeartbeat(context.Context,string,string,time.Time)error
    Restore(context.Context,Snapshot,Stage)error
    Finalize(context.Context,Snapshot,Stage)error
}

// Gate checks persisted host state, worker records and cleanup reservations.
// The caller must also hold a durable per-host update lease and drain.
type Gate interface {CheckDrained(context.Context,string)error}
type Progress interface {Step(string)}
type Request struct {
    HostID string
    Target Target
    RecoveryTimeout time.Duration
    HeartbeatTimeout time.Duration
}
type Outcome struct {
    State string
    Step string
    Err error
}
func report(p Progress,step string){if p!=nil{p.Step(step)}}

// Run is an updater transaction, NOT a complete remote-update mechanism.
// The privileged adapter, durable drain/lease, and verified release resolver
// have to be installed before any production action may invoke this code.
func Run(ctx context.Context,req Request,adapter Adapter,gate Gate,progress Progress)Outcome{
    fail:=func(step string,err error)Outcome{report(progress,step);return Outcome{State:"failed",Step:step,Err:err}}
    if adapter==nil||gate==nil||strings.TrimSpace(req.HostID)==""||strings.ContainsAny(req.HostID," \t\r\n/"){
        return fail("invalid-request",fmt.Errorf("%w: adapter, gate, and exact host ID required",ErrUnsafe))
    }
    if err:=req.Target.Validate();err!=nil{return fail("invalid-target",err)}
    report(progress,"checking-drain")
    if err:=gate.CheckDrained(ctx,req.HostID);err!=nil{return fail("drain-not-safe",fmt.Errorf("%w: %v",ErrUnsafe,err))}
    report(progress,"capturing-agent")
    prior,err:=adapter.Capture(ctx,req.HostID)
    if err!=nil{return fail("inspect-failed",err)}
    if prior.HostID!=req.HostID||!prior.SafeTopology||!prior.Running||prior.ContainerID==""{
        return fail("unsafe-agent-topology",fmt.Errorf("%w: exact running agent identity and supported topology required",ErrUnsafe))
    }
    report(progress,"pulling-image")
    if err:=adapter.PullAndVerify(ctx,req.Target);err!=nil{return fail("pull-or-verification-failed",err)}
    report(progress,"staging-replacement")
    staged,err:=adapter.StageReplacement(ctx,prior,req.Target)
    if err!=nil{
        // A Docker create may have succeeded even when post-create inspect
        // failed. The adapter returns the stage ID so a detached helper can
        // safely discard only that known, never-started container.
        if staged.ContainerID!=""&&staged.ContainerID!=prior.ContainerID{
            cleanupCtx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
            cleanupErr:=adapter.Discard(cleanupCtx,prior,staged)
            cancel()
            if cleanupErr!=nil{return Outcome{State:"review",Step:"staging-cleanup-unverified",Err:fmt.Errorf("%w: staging: %v; cleanup: %v",ErrReview,err,cleanupErr)}}
        }
        return fail("staging-failed",err)
    }
    if staged.ContainerID==""||staged.ContainerID==prior.ContainerID{
        return fail("invalid-staged-agent",fmt.Errorf("%w: replacement container identity was not verified",ErrUnsafe))
    }
    report(progress,"rechecking-drain")
    if err:=gate.CheckDrained(ctx,req.HostID);err!=nil{
        if cleanupErr:=adapter.Discard(ctx,prior,staged);cleanupErr!=nil{
            return Outcome{State:"review",Step:"discard-staged-unverified",Err:fmt.Errorf("%w: drain: %v; staged cleanup: %v",ErrReview,err,cleanupErr)}
        }
        return fail("drain-changed",fmt.Errorf("%w: %v",ErrUnsafe,err))
    }
    report(progress,"replacing-agent")
    swappedAt:=time.Now().UTC()
    replaceErr:=adapter.Replace(ctx,prior,staged)
    if replaceErr==nil{
        report(progress,"verifying-running-image")
        replaceErr=adapter.VerifyRunning(ctx,staged,req.Target)
    }
    if replaceErr==nil{
        report(progress,"waiting-for-same-host-heartbeat")
        heartbeatTTL:=req.HeartbeatTimeout
        if heartbeatTTL<=0||heartbeatTTL>2*time.Minute{heartbeatTTL=90*time.Second}
        verifyCtx,stop:=context.WithTimeout(ctx,heartbeatTTL)
        replaceErr=adapter.WaitForHeartbeat(verifyCtx,req.HostID,req.Target.Revision,swappedAt)
        stop()
    }
    if replaceErr!=nil{
        report(progress,"rolling-back")
        d:=req.RecoveryTimeout
        if d<=0||d>2*time.Minute{d=2*time.Minute}
        recovery,cancel:=context.WithTimeout(context.Background(),d)
        defer cancel()
        if rollbackErr:=adapter.Restore(recovery,prior,staged);rollbackErr!=nil{
            return Outcome{State:"review",Step:"rollback-unverified",Err:fmt.Errorf("%w: update: %v; rollback: %v",ErrReview,replaceErr,rollbackErr)}
        }
        return fail("rolled-back",fmt.Errorf("agent replacement failed and old container was restored: %w",replaceErr))
    }
    report(progress,"finalizing")
    if err:=adapter.Finalize(ctx,prior,staged);err!=nil{
        // Keep the healthy replacement and retained backup for review.
        return Outcome{State:"review",Step:"finalization-unverified",Err:fmt.Errorf("%w: %v",ErrReview,err)}
    }
    report(progress,"completed")
    return Outcome{State:"succeeded",Step:"completed"}
}
