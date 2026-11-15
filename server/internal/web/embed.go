package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var embeddedStatic embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
