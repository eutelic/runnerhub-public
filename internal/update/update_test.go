package update

import("crypto/ed25519";"crypto/rand";"encoding/base64";"testing")
func TestManifestSignature(t *testing.T){pub,priv,_:=ed25519.GenerateKey(rand.Reader);m:=Manifest{Version:"1.2.3",URL:"https://example.invalid/a",SHA256:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"};m.Signature=base64.StdEncoding.EncodeToString(ed25519.Sign(priv,m.signedPayload()));if err:=VerifyManifest(m,pub);err!=nil{t.Fatal(err)};m.Version="evil";if VerifyManifest(m,pub)==nil{t.Fatal("tamper accepted")}}
