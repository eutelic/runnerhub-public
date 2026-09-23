package ciaudit

import (
	"reflect"
	"testing"
)

func TestAnalyzeCriticalPathSeparatesLatencySavings(t *testing.T) {
	jobs:=[]CriticalJob{
		{ID:"plan",DurationSeconds:10},
		{ID:"test",Needs:[]string{"plan"},DurationSeconds:100},
		{ID:"duplicate",Needs:[]string{"plan"},DurationSeconds:80},
		{ID:"publish",Needs:[]string{"test","duplicate"},DurationSeconds:40},
	}
	got,err:=AnalyzeCriticalPath(jobs,map[string]bool{"test":true})
	if err!=nil{t.Fatal(err)}
	if got.TotalSeconds!=150{
		t.Fatalf("total=%v path=%v",got.TotalSeconds,got.Path)
	}
	if !reflect.DeepEqual(got.Path,[]string{"plan","test","publish"}){
		t.Fatalf("path=%v",got.Path)
	}
	if got.OptimizedSeconds!=130 || got.PotentialSavingsSeconds!=20{
		t.Fatalf("optimized=%v savings=%v",got.OptimizedSeconds,got.PotentialSavingsSeconds)
	}
}

func TestAnalyzeCriticalPathRejectsCycles(t *testing.T){
	_,err:=AnalyzeCriticalPath([]CriticalJob{{ID:"a",Needs:[]string{"b"}},{ID:"b",Needs:[]string{"a"}}},nil)
	if err==nil{t.Fatal("expected cycle error")}
}
