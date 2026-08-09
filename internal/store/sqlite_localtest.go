//go:build localtest

package store

import("context";"github.com/example-user/runnerhub/internal/core";"sync")
type SQLite struct{mu sync.Mutex;hosts map[string]core.Host;pools map[string]core.RunnerPool;workers map[string]core.Worker;audit []core.AuditEvent}
func OpenSQLite(string)(*SQLite,error){return &SQLite{hosts:map[string]core.Host{},pools:map[string]core.RunnerPool{},workers:map[string]core.Worker{}},nil}
func(s *SQLite)Close()error{return nil}
func(s *SQLite)UpsertHost(_ context.Context,v core.Host)error{s.mu.Lock();defer s.mu.Unlock();s.hosts[v.ID]=v;return nil}
func(s *SQLite)GetHost(_ context.Context,id string)(core.Host,error){s.mu.Lock();defer s.mu.Unlock();v,ok:=s.hosts[id];if !ok{return core.Host{},ErrNotFound};return v,nil}
func(s *SQLite)ListHosts(context.Context)([]core.Host,error){s.mu.Lock();defer s.mu.Unlock();out:=make([]core.Host,0,len(s.hosts));for _,v:=range s.hosts{out=append(out,v)};return out,nil}
func(s *SQLite)UpsertPool(_ context.Context,v core.RunnerPool)error{s.mu.Lock();defer s.mu.Unlock();s.pools[v.ID]=v;return nil}
func(s *SQLite)GetPool(_ context.Context,id string)(core.RunnerPool,error){s.mu.Lock();defer s.mu.Unlock();v,ok:=s.pools[id];if !ok{return core.RunnerPool{},ErrNotFound};return v,nil}
func(s *SQLite)ListPools(context.Context)([]core.RunnerPool,error){s.mu.Lock();defer s.mu.Unlock();out:=make([]core.RunnerPool,0,len(s.pools));for _,v:=range s.pools{out=append(out,v)};return out,nil}
func(s *SQLite)UpsertWorker(_ context.Context,v core.Worker)error{s.mu.Lock();defer s.mu.Unlock();s.workers[v.ID]=v;return nil}
func(s *SQLite)GetWorker(_ context.Context,id string)(core.Worker,error){s.mu.Lock();defer s.mu.Unlock();v,ok:=s.workers[id];if !ok{return core.Worker{},ErrNotFound};return v,nil}
func(s *SQLite)ListWorkers(context.Context)([]core.Worker,error){s.mu.Lock();defer s.mu.Unlock();out:=make([]core.Worker,0,len(s.workers));for _,v:=range s.workers{out=append(out,v)};return out,nil}
func(s *SQLite)AppendAudit(_ context.Context,v core.AuditEvent)error{s.mu.Lock();defer s.mu.Unlock();s.audit=append(s.audit,v);return nil}
func(s *SQLite)ListAudit(_ context.Context,limit int)([]core.AuditEvent,error){s.mu.Lock();defer s.mu.Unlock();if limit<=0||limit>len(s.audit){limit=len(s.audit)};out:=append([]core.AuditEvent(nil),s.audit[len(s.audit)-limit:]...);return out,nil}
