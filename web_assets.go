package kigo

import (
	"embed"
	"io/fs"
)

//go:embed web/*
var embeddedWeb embed.FS

func EmbeddedWebFS() fs.FS {
	sub, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		panic(err)
	}
	return sub
}
