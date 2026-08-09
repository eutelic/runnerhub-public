package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"github.com/example-user/runnerhub/internal/core"
	"github.com/example-user/runnerhub/internal/store"
)
var ErrNoCapacity=errors.New("no compatible host capacity")
type Decision struct{Host core.Host `json:"host"`;Reason string `json:"reason"`;Used core.Resources `json:"used"`};type Scheduler struct{Store store.Store}
func(s Scheduler)SelectHost(ctx context.Context,pool core.RunnerPool)(Decision,error){hosts,err:=s.Store.ListHosts(ctx);if err!=nil{return Decision{},err};workers,err:=s.Store.ListWorkers(ctx);if err!=nil{return Decision{},err};used:=map[string]core.Resources{};activeCount:=map[string]int{};for _,w:=range workers{if terminal(w.State){continue};used[w.HostID]=core.Add(used[w.HostID],w.Resources);activeCount[w.HostID]++};type candidate struct{h core.Host;used core.Resources;score int64};var candidates []candidate;var rejects []string;for _,h:=range hosts{if h.State!=core.HostOnline{rejects=append(rejects,h.Name+": not online");continue};if !matchTags(h.Tags,pool.HostSelector){rejects=append(rejects,h.Name+": selector mismatch");continue};if !supportsIsolation(h,pool.Isolation){rejects=append(rejects,h.Name+": isolation unsupported");continue};if activeCount[h.ID]>=pool.MaxConcurrency{rejects=append(rejects,h.Name+": concurrency exhausted");continue};u:=used[h.ID];if !core.Fits(h.Capacity,u,pool.Resources){rejects=append(rejects,h.Name+": resources exhausted");continue};score:=(h.Capacity.CPUMillis-u.CPUMillis-pool.Resources.CPUMillis)+(h.Capacity.MemoryBytes-u.MemoryBytes-pool.Resources.MemoryBytes)/(1024*1024);candidates=append(candidates,candidate{h:h,used:u,score:score})};if len(candidates)==0{return Decision{},fmt.Errorf("%w: %s",ErrNoCapacity,strings.Join(rejects,"; "))};sort.SliceStable(candidates,func(i,j int)bool{return candidates[i].score>candidates[j].score});c:=candidates[0];return Decision{Host:c.h,Used:c.used,Reason:"best compatible online host with available requested capacity"},nil}
func terminal(s core.WorkerState)bool{return s==core.WorkerDeleted||s==core.WorkerCompleted||s==core.WorkerFailed}
func matchTags(tags,sel map[string]string)bool{for k,v:=range sel{if tags[k]!=v{return false}};return true}
func supportsIsolation(h core.Host,mode core.IsolationMode)bool{if h.Tags==nil{return mode==core.IsolationContainerTrusted};key:="isolation."+string(mode);if v,ok:=h.Tags[key];ok{return v=="true"};return mode==core.IsolationContainerTrusted&&h.Provider=="docker"}
