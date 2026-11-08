package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"github.com/eutelic/runnerhub/internal/auth"
)

func decode(w http.ResponseWriter,r *http.Request,v any)bool{defer r.Body.Close();d:=json.NewDecoder(io.LimitReader(r.Body,1<<20));d.DisallowUnknownFields();if err:=d.Decode(v);err!=nil{problem(w,400,err);return false};var extra any;if err:=d.Decode(&extra);err!=io.EOF{if err==nil{err=errors.New("request body must contain exactly one JSON value")};problem(w,400,err);return false};return true}
func writeJSON(w http.ResponseWriter,status int,v any){w.Header().Set("Content-Type","application/json");w.WriteHeader(status);_=json.NewEncoder(w).Encode(v)}
func problem(w http.ResponseWriter,status int,err error){writeJSON(w,status,map[string]any{"error":http.StatusText(status),"detail":truncate(err.Error())})}
func bearer(r *http.Request)string{v:=r.Header.Get("Authorization");if strings.HasPrefix(v,"Bearer "){return strings.TrimSpace(strings.TrimPrefix(v,"Bearer "))};return ""}
func randomID()string{b:=make([]byte,8);_,_=rand.Read(b);return base64.RawURLEncoding.EncodeToString(b)}
func truncate(v string)string{if len(v)>512{return v[:512]};return v}
func actor(ctx context.Context)string{p:=auth.PrincipalFrom(ctx);if p.Name!=""{return p.Name};return "api"}
func firstNonEmpty(a,b string)string{if strings.TrimSpace(a)!=""{return a};return b}
func securityHeaders(next http.Handler)http.Handler{return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("X-Content-Type-Options","nosniff");w.Header().Set("X-Frame-Options","DENY");w.Header().Set("Referrer-Policy","no-referrer");w.Header().Set("Content-Security-Policy","default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'");if r.TLS!=nil{w.Header().Set("Strict-Transport-Security","max-age=31536000")};next.ServeHTTP(w,r)})}
