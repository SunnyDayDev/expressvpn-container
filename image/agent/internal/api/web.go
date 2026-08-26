package api

import (
	"io/fs"
	"net/http"
	"strings"
)

// spaHandler отдаёт статику SPA; любой путь без файла (клиентский роутинг)
// получает index.html. Никаких внешних ресурсов — всё из встроенной ФС.
func spaHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServerFS(webFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(webFS, path); err != nil {
			// Клиентский роут (/settings, /login, …) → index.html.
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
