package store

import("context";"errors";"github.com/example-user/runnerhub/internal/core")
var ErrNotFound=errors.New("store: not found")
type Store interface{UpsertHost(context.Context,core.Host)error;GetHost(context.Context,string)(core.Host,error);ListHosts(context.Context)([]core.Host,error);UpsertPool(context.Context,core.RunnerPool)error;GetPool(context.Context,string)(core.RunnerPool,error);ListPools(context.Context)([]core.RunnerPool,error);UpsertWorker(context.Context,core.Worker)error;GetWorker(context.Context,string)(core.Worker,error);ListWorkers(context.Context)([]core.Worker,error);AppendAudit(context.Context,core.AuditEvent)error;ListAudit(context.Context,int)([]core.AuditEvent,error);Close()error}
