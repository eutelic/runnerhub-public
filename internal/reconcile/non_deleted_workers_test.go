package reconcile

import (
    "context"
    "errors"
    "testing"

    "github.com/eutelic/runnerhub/internal/core"
    "github.com/eutelic/runnerhub/internal/store"
)

type nonDeletedReaderSpy struct {
    store.Store
    called bool
}

func (s *nonDeletedReaderSpy)ListNonDeletedWorkers(context.Context)([]core.Worker,error){
    s.called=true
    return []core.Worker{},nil
}

func (s *nonDeletedReaderSpy)ListWorkers(context.Context)([]core.Worker,error){
    return nil,errors.New("full historical worker scan must not be called")
}

func TestReconcilerUsesIndexedNonDeletedSnapshot(t *testing.T){
    st:=&nonDeletedReaderSpy{}
    r:=&Reconciler{Store:st}
    if err:=r.Once(context.Background());err!=nil{t.Fatal(err)}
    if !st.called {t.Fatal("reconciler did not request the non-deleted worker snapshot")}
}
