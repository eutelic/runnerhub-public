package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html
var content embed.FS

func Handler() http.Handler { sub, _ := fs.Sub(content, "."); return http.FileServer(http.FS(sub)) }
