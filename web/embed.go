package web

import(
"embed";"io/fs";"net/http")

//go:embed index.html app.js style.css diagnostics.html diagnostics.js queue-diagnostics.js incident-workbench.js runnerhub-logo.png runnerhub-logo-black.png
var content embed.FS

func Handler()http.Handler{sub,_:=fs.Sub(content,".");files:=http.FileServer(http.FS(sub));return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Security-Policy","default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'");files.ServeHTTP(w,r)})}
