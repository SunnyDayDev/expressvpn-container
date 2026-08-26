// Package webui — встроенная статика веб-интерфейса (Svelte SPA).
// Реальный dist/ собирается node-стейджем Dockerfile; в репозитории лежит
// заглушка, чтобы go build работал без Node.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS возвращает файловую систему статики (корень — содержимое dist/).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
