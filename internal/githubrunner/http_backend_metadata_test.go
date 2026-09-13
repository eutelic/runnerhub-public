package githubrunner

import("context";"net/http";"net/http/httptest";"testing";"github.com/eutelic/runnerhub/internal/core")
func TestHTTPBackendCapturesRunnerVersionMetadata(t *testing.T){s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){_,_=w.Write([]byte(`{"id":42,"name":"r","os":"linux","status":"online","busy":false,"ephemeral":true,"version":"2.336.0","labels":[]}`))}));defer s.Close();b,_:=NewHTTPBackendWithBaseURL(s.Client(),staticTokenSource("secret"),s.URL);got,err:=b.GetRunner(context.Background(),core.GitHubScope{Kind:core.ScopeRepository,Owner:"o",Repository:"r"},42);if err!=nil{t.Fatal(err)};if got.Version!="2.336.0"||got.OS!="linux"||!got.Ephemeral{t.Fatalf("metadata=%+v",got)}}
