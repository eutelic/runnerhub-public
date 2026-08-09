//go:build !localtest

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"github.com/example-user/runnerhub/internal/core"
	_ "modernc.org/sqlite"
)
type SQLite struct{db *sql.DB}
func OpenSQLite(path string)(*SQLite,error){db,err:=sql.Open("sqlite",path);if err!=nil{return nil,err};db.SetMaxOpenConns(1);s:=&SQLite{db:db};if err:=s.migrate(context.Background());err!=nil{_=db.Close();return nil,err};return s,nil}
func(s *SQLite)Close()error{return s.db.Close()}
func(s *SQLite)migrate(ctx context.Context)error{_,err:=s.db.ExecContext(ctx,`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS hosts(id TEXT PRIMARY KEY, data BLOB NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pools(id TEXT PRIMARY KEY, data BLOB NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS workers(id TEXT PRIMARY KEY, data BLOB NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS audit_events(id INTEGER PRIMARY KEY AUTOINCREMENT, actor TEXT NOT NULL, action TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, result TEXT NOT NULL, safe_detail TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_audit_created_at ON audit_events(created_at DESC);`);return err}
func marshal(v any)([]byte,error){return json.Marshal(v)}
func decode[T any](b []byte)(T,error){var v T;err:=json.Unmarshal(b,&v);return v,err}
func upsert(ctx context.Context,db *sql.DB,table,id string,v any)error{b,err:=marshal(v);if err!=nil{return err};q:=fmt.Sprintf("INSERT INTO %s(id,data,updated_at) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at",table);_,err=db.ExecContext(ctx,q,id,b,time.Now().UTC().Format(time.RFC3339Nano));return err}
func get[T any](ctx context.Context,db *sql.DB,table,id string)(T,error){var b []byte;q:=fmt.Sprintf("SELECT data FROM %s WHERE id=?",table);err:=db.QueryRowContext(ctx,q,id).Scan(&b);if errors.Is(err,sql.ErrNoRows){var z T;return z,ErrNotFound};if err!=nil{var z T;return z,err};return decode[T](b)}
func list[T any](ctx context.Context,db *sql.DB,table string)([]T,error){q:=fmt.Sprintf("SELECT data FROM %s ORDER BY updated_at DESC",table);rows,err:=db.QueryContext(ctx,q);if err!=nil{return nil,err};defer rows.Close();out:=[]T{};for rows.Next(){var b []byte;if err:=rows.Scan(&b);err!=nil{return nil,err};v,err:=decode[T](b);if err!=nil{return nil,err};out=append(out,v)};return out,rows.Err()}
func(s *SQLite)UpsertHost(ctx context.Context,v core.Host)error{return upsert(ctx,s.db,"hosts",v.ID,v)}
func(s *SQLite)GetHost(ctx context.Context,id string)(core.Host,error){return get[core.Host](ctx,s.db,"hosts",id)}
func(s *SQLite)ListHosts(ctx context.Context)([]core.Host,error){return list[core.Host](ctx,s.db,"hosts")}
func(s *SQLite)UpsertPool(ctx context.Context,v core.RunnerPool)error{return upsert(ctx,s.db,"pools",v.ID,v)}
func(s *SQLite)GetPool(ctx context.Context,id string)(core.RunnerPool,error){return get[core.RunnerPool](ctx,s.db,"pools",id)}
func(s *SQLite)ListPools(ctx context.Context)([]core.RunnerPool,error){return list[core.RunnerPool](ctx,s.db,"pools")}
func(s *SQLite)UpsertWorker(ctx context.Context,v core.Worker)error{return upsert(ctx,s.db,"workers",v.ID,v)}
func(s *SQLite)GetWorker(ctx context.Context,id string)(core.Worker,error){return get[core.Worker](ctx,s.db,"workers",id)}
func(s *SQLite)ListWorkers(ctx context.Context)([]core.Worker,error){return list[core.Worker](ctx,s.db,"workers")}
func(s *SQLite)AppendAudit(ctx context.Context,e core.AuditEvent)error{if e.CreatedAt.IsZero(){e.CreatedAt=time.Now().UTC()};_,err:=s.db.ExecContext(ctx,"INSERT INTO audit_events(actor,action,resource_type,resource_id,result,safe_detail,created_at) VALUES(?,?,?,?,?,?,?)",e.Actor,e.Action,e.ResourceType,e.ResourceID,e.Result,e.SafeDetail,e.CreatedAt.Format(time.RFC3339Nano));return err}
func(s *SQLite)ListAudit(ctx context.Context,limit int)([]core.AuditEvent,error){if limit<=0||limit>1000{limit=100};rows,err:=s.db.QueryContext(ctx,"SELECT id,actor,action,resource_type,resource_id,result,safe_detail,created_at FROM audit_events ORDER BY id DESC LIMIT ?",limit);if err!=nil{return nil,err};defer rows.Close();var out []core.AuditEvent;for rows.Next(){var e core.AuditEvent;var created string;if err:=rows.Scan(&e.ID,&e.Actor,&e.Action,&e.ResourceType,&e.ResourceID,&e.Result,&e.SafeDetail,&created);err!=nil{return nil,err};e.CreatedAt,_=time.Parse(time.RFC3339Nano,created);out=append(out,e)};return out,rows.Err()}
