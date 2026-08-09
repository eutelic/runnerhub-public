package images

import "testing"
func TestPinnedDigest(t *testing.T){if ValidatePinned("ghcr.io/actions/actions-runner@sha256:"+repeat("a",64))!=nil{t.Fatal("valid digest rejected")};if ValidatePinned("ghcr.io/actions/actions-runner:latest")==nil{t.Fatal("tag must not be accepted as pinned")}}
func repeat(s string,n int)string{out:="";for range n{out+=s};return out}
