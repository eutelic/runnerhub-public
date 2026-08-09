package auth

import (
	"net/http"
	"testing"
)
func TestNoKeysOnlyAllowsLoopbackAdmin(t *testing.T){m:=New(Config{});local:=&http.Request{RemoteAddr:"127.0.0.1:1234"};p,err:=m.Authenticate(local);if err!=nil||p.Role!=RoleAdmin{t.Fatalf("local auth=%+v err=%v",p,err)};remote:=&http.Request{RemoteAddr:"10.0.0.5:1234"};if _,err:=m.Authenticate(remote);err==nil{t.Fatal("unauthenticated remote API access was allowed")}}
func TestOIDCHeadersRequireLoopbackProxy(t *testing.T){m:=New(Config{TrustOIDCHeaders:true});r:=&http.Request{RemoteAddr:"10.0.0.5:1234",Header:make(http.Header)};r.Header.Set("X-RunnerHub-User","attacker");r.Header.Set("X-RunnerHub-Role","admin");if _,err:=m.Authenticate(r);err==nil{t.Fatal("trusted headers from remote client were accepted")};r.RemoteAddr="127.0.0.1:1234";p,err:=m.Authenticate(r);if err!=nil||p.Name!="attacker"||p.Role!=RoleAdmin{t.Fatalf("loopback proxy auth=%+v err=%v",p,err)}}
func TestParseKeysDropsInvalidRoles(t *testing.T){got:=ParseKeys("good:operator:ops,bad:not-a-role:nope");if len(got)!=1||got["good"].Role!=RoleOperator{t.Fatalf("unexpected keys: %#v",got)}}
