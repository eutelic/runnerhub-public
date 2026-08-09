package policy

import("testing";"github.com/example-user/runnerhub/internal/core")
func TestAllowlistChecksOnlyAssignedIdentity(t *testing.T){p:=core.RunnerPool{AllowedRepositories:[]string{"acme/app"},AllowedWorkflows:[]string{"acme/app/.github/workflows/build.yml@main"}};e:=Engine{};if err:=e.Evaluate(Request{Pool:p});err!=nil{t.Fatalf("prewarmed worker should rely on GitHub group policy: %v",err)};if e.Evaluate(Request{Pool:p,Repository:"acme/other"})==nil{t.Fatal("concrete disallowed repository accepted")};if e.Evaluate(Request{Pool:p,Repository:"acme/app",Workflow:"acme/app/.github/workflows/other.yml@main"})==nil{t.Fatal("concrete disallowed workflow accepted")}}
