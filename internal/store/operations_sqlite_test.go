//go:build !localtest

package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testOperation(id string) Operation {
	now:=time.Now().UTC()
	return Operation{
		ID:id,Kind:"host-bootstrap",Target:"compute",Actor:"admin",
		State:"queued",Step:"request-accepted",Detail:"Request accepted",
		StartedAt:now,UpdatedAt:now,LastProgressAt:now,
		Events:[]OperationEvent{{State:"queued",Step:"request-accepted",At:now}},
	}
}

func TestOperationPersistenceAndDeduplication(t *testing.T){
	ctx:=context.Background()
	path:=filepath.Join(t.TempDir(),"runnerhub.sqlite")
	st,err:=OpenSQLite(path)
	if err!=nil{t.Fatal(err)}
	first:=testOperation("op-one")
	got,reused,err:=st.StartOperation(ctx,first)
	if err!=nil||reused||got.ID!="op-one"{t.Fatalf("first=%+v reused=%v err=%v",got,reused,err)}
	second:=testOperation("op-two")
	got,reused,err=st.StartOperation(ctx,second)
	if err!=nil||!reused||got.ID!="op-one"{t.Fatalf("second=%+v reused=%v err=%v",got,reused,err)}
	if err:=st.Close();err!=nil{t.Fatal(err)}

	reopened,err:=OpenSQLite(path)
	if err!=nil{t.Fatal(err)}
	defer reopened.Close()
	loaded,err:=reopened.GetOperation(ctx,"op-one")
	if err!=nil||loaded.State!="unknown"||loaded.Step!="server-restarted"{
		t.Fatalf("restarted operation must be unknown: %+v err=%v",loaded,err)
	}
	if len(loaded.Events)!=2{t.Fatalf("restart lost event history: %+v",loaded.Events)}
	got,reused,err=reopened.StartOperation(ctx,second)
	if err!=nil||reused||got.ID!="op-two"{t.Fatalf("new after restart=%+v reused=%v err=%v",got,reused,err)}
	got,reused,err=reopened.StartOperation(ctx,testOperation("op-three"))
	if err!=nil||!reused||got.ID!="op-two"{t.Fatalf("duplicate after restart=%+v reused=%v err=%v",got,reused,err)}
	second.State="succeeded"
	second.Step="agent-ready"
	second.UpdatedAt=time.Now().UTC()
	if err:=reopened.UpdateOperation(ctx,second);err!=nil{t.Fatal(err)}
	third:=testOperation("op-three")
	got,reused,err=reopened.StartOperation(ctx,third)
	if err!=nil||reused||got.ID!="op-three"{t.Fatalf("after completion=%+v reused=%v err=%v",got,reused,err)}
}

func TestStaleOperationBecomesUnknownButRetainsHistory(t *testing.T){
	ctx:=context.Background()
	st,err:=OpenSQLite(":memory:")
	if err!=nil{t.Fatal(err)}
	defer st.Close()
	op:=testOperation("op-stale")
	if _,_,err:=st.StartOperation(ctx,op);err!=nil{t.Fatal(err)}
	op.UpdatedAt=time.Now().Add(-13*time.Minute).UTC()
	if err:=st.UpdateOperation(ctx,op);err!=nil{t.Fatal(err)}
	loaded,err:=st.GetOperation(ctx,op.ID)
	if err!=nil{t.Fatal(err)}
	if loaded.State!="unknown"||loaded.Step!="outcome-unknown"||loaded.EndedAt==nil{
		t.Fatalf("stale=%+v",loaded)
	}
	if len(loaded.Events)!=2||loaded.Events[0].Step!="request-accepted"{
		t.Fatalf("history was lost: %+v",loaded.Events)
	}
	if !strings.Contains(loaded.Detail,"Verify the host"){t.Fatalf("detail=%q",loaded.Detail)}
	next:=testOperation("op-next")
	got,reused,err:=st.StartOperation(ctx,next)
	if err!=nil||reused||got.ID!="op-next"{t.Fatalf("next=%+v reused=%v err=%v",got,reused,err)}
}
