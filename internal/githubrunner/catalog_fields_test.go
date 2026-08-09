package githubrunner

import("context";"fmt";"net/http";"net/http/httptest";"testing")
func TestRunnerGroupSnakeCaseSecurityFields(t *testing.T){s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){fmt.Fprint(w,`{"id":7,"name":"secure","visibility":"selected","allows_public_repositories":true,"restricted_to_workflows":true,"selected_workflows":["acme/app/.github/workflows/build.yml@main"]}`)}));defer s.Close();c,err:=NewHTTPCatalog(s.Client(),staticTokenSource("token"),s.URL);if err!=nil{t.Fatal(err)};g,err:=c.GetRunnerGroup(context.Background(),"acme",7);if err!=nil{t.Fatal(err)};if !g.AllowsPublicRepositories||!g.RestrictedToWorkflows||len(g.SelectedWorkflows)!=1{t.Fatalf("security fields not decoded: %+v",g)}}
