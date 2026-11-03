package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eutelic/runnerhub/internal/auth"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/machinebootstrap"
	"github.com/eutelic/runnerhub/internal/store"
)

func validateBootstrapInput(in *bootstrapHostRequest) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Address = strings.TrimSpace(in.Address)
	in.User = strings.TrimSpace(in.User)
	in.HostKeyFingerprint = strings.TrimSpace(in.HostKeyFingerprint)
	if !validTagName(in.Name) {
		return errors.New("machine name must contain only letters, numbers, dot, underscore, or dash")
	}
	if in.Address == "" || in.User == "" {
		return errors.New("machine address and SSH user are required")
	}
	if in.HostKeyFingerprint == "" {
		return errors.New("SSH host key fingerprint is required; scan and verify it before bootstrap")
	}
	if !strings.HasPrefix(in.HostKeyFingerprint, "SHA256:") ||
		strings.ContainsAny(in.HostKeyFingerprint, " \t\r\n") {
		return errors.New("SSH host fingerprint must use SHA256:<fingerprint> format")
	}
	if in.Priority < -1000 || in.Priority > 1000 {
		return errors.New("host priority must be between -1000 and 1000")
	}
	mode:=strings.TrimSpace(in.Mode)
	if mode!="" && mode!="docker" && mode!="android-emulator" {return errors.New("unsupported machine setup mode")}
	in.Mode=mode
	if mode=="android-emulator" {
		if in.AllowPrivilegedWorkers {return errors.New("native Android KVM agent cannot enable publisher/DinD privileges")}
		for _,tag:=range in.Tags {
			if strings.EqualFold(strings.TrimSpace(tag),core.AndroidEmulatorCapabilityTag) {
				return errors.New("hardware.kvm must be system-verified, not supplied as a user tag")
			}
		}
	}
	_, err := tagMap(in.Tags)
	return err
}

func (s *Service) runHostBootstrap(ctx context.Context, in bootstrapHostRequest) (core.Host, error) {
	tags, err := tagMap(in.Tags)
	if err != nil {
		return core.Host{}, err
	}
	nativeAndroid:=in.Mode=="android-emulator"
	if nativeAndroid {tags["android-emulator"]="true"}
	before, err := s.Store.ListHosts(ctx)
	if err != nil {
		return core.Host{}, err
	}
	existing := make(map[string]struct{}, len(before))
	for _, h := range before {
		existing[h.ID] = struct{}{}
		if nativeAndroid && strings.EqualFold(h.Name,in.Name) && h.State==core.HostOnline {
			return core.Host{},errors.New("an online host already uses this name; use a separate identity such as compute-android")
		}
	}
	ttl:=10*time.Minute
	if nativeAndroid {ttl=20*time.Minute}
	token, err := s.Hub.IssueEnrollmentToken(in.Name, tags, ttl)
	if err != nil {
		return core.Host{}, err
	}
	started := time.Now().UTC()
	image := strings.TrimSpace(s.MachineAgentImage)
	if image == "" {
		image = "ghcr.io/eutelic/runnerhub-agent:latest"
	}
	sudoPassword := in.SudoPassword
	if sudoPassword == "" {
		sudoPassword = in.Password
	}
	installDocker := true
	if in.InstallDocker != nil {
		installDocker = *in.InstallDocker
	}
	req := machinebootstrap.Request{
		Address:in.Address, User:in.User, Password:in.Password, SudoPassword:sudoPassword,
		PrivateKey:in.PrivateKey, HostKeyFingerprint:in.HostKeyFingerprint,
		RegistryUsername:in.RegistryUsername, RegistryPassword:in.RegistryPassword,
		HostName:in.Name, ControlPlaneURL:s.MachineBootstrapURL, AgentImage:image,
		InstallDocker:installDocker,
		AllowInsecureHTTP:strings.HasPrefix(strings.ToLower(s.MachineBootstrapURL),"http://"),
		AllowPrivilegedWorkers:in.AllowPrivilegedWorkers,
		AndroidEmulator:nativeAndroid,
	}
	host, err := s.MachineBootstrap.Bootstrap(ctx,req,token,func(ctx context.Context,name string)(core.Host,error){
		return machinebootstrap.WaitTimeout(ctx,45*time.Second,func()(core.Host,bool,error){
			hosts,err:=s.Store.ListHosts(ctx)
			if err!=nil{return core.Host{},false,err}
			for _,h:=range hosts{
				if _,ok:=existing[h.ID];ok{continue}
				if h.Name==name&&!h.LastSeenAt.Before(started){return h,true,nil}
			}
			return core.Host{},false,nil
		})
	})
	if err!=nil{return host,err}
	if nativeAndroid && (host.Provider!="process" || host.SystemTags[core.AndroidEmulatorCapabilityTag]!="true") {
		return host,errors.New("native Android agent lacks a verified process provider and hardware.kvm capability")
	}
	if host.Priority!=in.Priority{
		host.Priority=in.Priority
		if err:=s.Store.UpsertHost(ctx,host);err!=nil{return core.Host{},err}
	}
	return host,nil
}

func (s *Service) operationStore() (store.OperationStore, bool) {
	v, ok := s.Store.(store.OperationStore)
	return v,ok
}

func newHostOperation(name,actorName string) store.Operation {
	now:=time.Now().UTC()
	return store.Operation{
		ID:"op-"+randomID(),Kind:"host-bootstrap",Target:strings.ToLower(name),
		Actor:actorName,State:"queued",Step:"request-accepted",
		Detail:"Enrollment request accepted. Waiting for remote preparation.",
		StartedAt:now,UpdatedAt:now,LastProgressAt:now,
		Events:[]store.OperationEvent{{State:"queued",Step:"request-accepted",At:now}},
	}
}

func (s *Service) startHostBootstrapOperation(w http.ResponseWriter,r *http.Request){
	if auth.PrincipalFrom(r.Context()).Role < auth.RoleAdmin {
		problem(w,http.StatusForbidden,errors.New("machine bootstrap requires admin access"))
		return
	}
	if s.MachineBootstrap==nil||strings.TrimSpace(s.MachineBootstrapURL)==""||s.Hub==nil {
		problem(w,http.StatusServiceUnavailable,errors.New("machine bootstrap is not configured"))
		return
	}
	st,ok:=s.operationStore()
	if !ok {
		problem(w,http.StatusServiceUnavailable,errors.New("operation persistence is unavailable"))
		return
	}
	var in bootstrapHostRequest
	if !decode(w,r,&in){return}
	if err:=validateBootstrapInput(&in);err!=nil{
		problem(w,http.StatusBadRequest,err)
		return
	}
	op:=newHostOperation(in.Name,actor(r.Context()))
	active,reused,err:=st.StartOperation(r.Context(),op)
	if err!=nil{problem(w,http.StatusInternalServerError,errors.New("failed to create enrollment operation"));return}
	if !reused {
		// No SSH credentials are written to the operations table. The request
		// remains only in this goroutine and is discarded when it completes.
		// #nosec G118 -- Intentional detached, bounded enrollment worker: performHostBootstrapOperation owns a bounded mode-dependent timeout and preserves state after the HTTP response.
		go s.performHostBootstrapOperation(active.ID,in,active.Actor)
	}
	writeJSON(w,http.StatusAccepted,struct{
		store.Operation
		Reused bool `json:"reused"`
	}{Operation:active,Reused:reused})
}

func (s *Service) transitionHostOperation(ctx context.Context,id,state,step,detail,hostID string) {
	st,ok:=s.operationStore()
	if !ok{return}
	op,err:=st.GetOperation(ctx,id)
	if err!=nil{return}
	if op.State=="succeeded"||op.State=="failed"||op.State=="blocked"||op.State=="unknown"{return}
	now:=time.Now().UTC()
	op.State=state
	op.Step=step
	op.Detail=detail
	op.UpdatedAt=now
	op.LastProgressAt=now
	op.HostID=hostID
	if state=="succeeded"||state=="failed"||state=="blocked"||state=="unknown"{
		op.EndedAt=&now
	}
	op.Events=append(op.Events,store.OperationEvent{State:state,Step:step,At:now})
	if len(op.Events)>32{op.Events=op.Events[len(op.Events)-32:]}
	_=st.UpdateOperation(ctx,op)
}

func (s *Service) performHostBootstrapOperation(id string,in bootstrapHostRequest,initiator string){
	timeout:=8*time.Minute
	if in.Mode=="android-emulator" {timeout=16*time.Minute}
	ctx,cancel:=context.WithTimeout(context.Background(),timeout)
	defer cancel()
	s.transitionHostOperation(ctx,id,"working","preparing-enrollment",
		"Preparing an enrollment token and checking the existing host identities.","")
	ctx=machinebootstrap.WithProgress(ctx,func(step string){
		state:="working"
		detail:="Remote setup is in progress; no byte-level transfer telemetry is available."
		switch step{
		case "verifying-host-key":
			detail="Verifying the supplied SSH host-key fingerprint against the remote machine."
		case "configuring-remote-host":
			detail="Running remote prerequisites, Docker image pull and agent startup. This may take several minutes."
		case "waiting-for-agent":
			state="waiting"
			detail="Remote setup returned. Waiting up to 45 seconds for a new agent heartbeat."
		case "cleaning-enrollment-secret":
			detail="Agent was observed. Finalizing the container without the temporary enrollment token."
		default:
			return
		}
		s.transitionHostOperation(ctx,id,state,step,detail,"")
	})
	host,err:=s.runHostBootstrap(ctx,in)
	// A timeout context cannot be used to persist the final operation state.
	finalCtx,stop:=context.WithTimeout(context.Background(),5*time.Second)
	defer stop()
	if err!=nil{
		step:="bootstrap-failed"
		state:="failed"
		detail:="Remote bootstrap failed. Check SSH access, KVM permissions if applicable, Docker and agent logs before retrying."
		if host.ID!=""{
			state="blocked"
			if in.Mode=="android-emulator" && (host.Provider!="process" || host.SystemTags[core.AndroidEmulatorCapabilityTag]!="true") {
				step="native-kvm-unverified"
				detail="Native agent responded but did not report the system-verified hardware.kvm capability. Check /dev/kvm access for runnerhub-android and the agent revision."
			}else{
				step="enrollment-cleanup-failed"
				detail="The new agent was observed, but final enrollment-secret cleanup failed. Check the remote agent and remove the temporary credential before retrying."
			}
		}else if ctx.Err()!=nil{
			state="unknown"
			step="bootstrap-timed-out"
			detail="The server-side time limit was reached. Remote changes may have completed; check the agent before retrying."
		}
		s.transitionHostOperation(finalCtx,id,state,step,detail,host.ID)
		_=s.Store.AppendAudit(finalCtx,core.AuditEvent{
			Actor:initiator,Action:"bootstrap_host",ResourceType:"host",ResourceID:host.ID,
			Result:state,SafeDetail:"Tracked bootstrap did not confirm safe readiness; inspect the host before retrying.",
		})
		return
	}
	s.transitionHostOperation(finalCtx,id,"succeeded","agent-ready",
		"New agent identity confirmed. Review earlier identities separately before cleanup.",host.ID)
	_=s.Store.AppendAudit(finalCtx,core.AuditEvent{
		Actor:initiator,Action:"bootstrap_host",ResourceType:"host",
		ResourceID:host.ID,Result:"ok",SafeDetail:in.Name,
	})
}

func (s *Service) getHostOperation(w http.ResponseWriter,r *http.Request){
	if auth.PrincipalFrom(r.Context()).Role<auth.RoleAdmin {
		problem(w,http.StatusForbidden,errors.New("operations require admin access"));return
	}
	st,ok:=s.operationStore()
	if !ok{problem(w,503,errors.New("operation persistence unavailable"));return}
	op,err:=st.GetOperation(r.Context(),r.PathValue("id"))
	if errors.Is(err,store.ErrNotFound){problem(w,404,errors.New("operation not found"));return}
	if err!=nil{problem(w,500,errors.New("operation lookup failed"));return}
	writeJSON(w,200,op)
}

func (s *Service) listHostOperations(w http.ResponseWriter,r *http.Request){
	if auth.PrincipalFrom(r.Context()).Role<auth.RoleAdmin {
		problem(w,http.StatusForbidden,errors.New("operations require admin access"));return
	}
	st,ok:=s.operationStore()
	if !ok{problem(w,503,errors.New("operation persistence unavailable"));return}
	limit,_:=strconv.Atoi(r.URL.Query().Get("limit"))
	items,err:=st.ListOperations(r.Context(),limit)
	if err!=nil{problem(w,500,errors.New("operation listing failed"));return}
	writeJSON(w,200,items)
}
