package images
import"testing"
func TestPinnedImage(t *testing.T){d:="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";if err:=ValidatePinned("registry.example:5000/team/runner@sha256:"+d);err!=nil{t.Fatal(err)};if ValidatePinned("ghcr.io/actions/actions-runner:latest")==nil{t.Fatal("tag-only image must not be accepted as pinned")}}
