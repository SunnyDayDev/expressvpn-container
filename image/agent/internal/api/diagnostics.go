package api

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"detour/agent/internal/config"
)

// handleDiagnosticsArchive — zip с состоянием, конфигурацией (в редакции) и
// журналом. Секреты не попадают: конфигурация маскируется, журнал уже
// отредактирован на входе (пакет logs).
func (s *Server) handleDiagnosticsArchive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=detour-diagnostics-%s.zip", time.Now().UTC().Format("20060102-150405")))

	zw := zip.NewWriter(w)
	writeEntry := func(name string, data []byte) {
		f, err := zw.Create(name)
		if err != nil {
			return
		}
		f.Write(data)
	}

	stateRaw, _ := json.MarshalIndent(s.deps.State.Get(), "", "  ")
	writeEntry("state.json", stateRaw)

	cfgRaw, _ := json.MarshalIndent(config.Redacted(s.deps.Config.Get()), "", "  ")
	writeEntry("config.json", cfgRaw)

	verRaw, _ := json.MarshalIndent(s.deps.Version, "", "  ")
	writeEntry("version.json", verRaw)

	var logsBuf []byte
	for _, e := range s.deps.Logs.Query("", time.Time{}, 5000) {
		logsBuf = append(logsBuf, fmt.Sprintf("%s %-5s [%s] %s\n",
			e.Time.Format(time.RFC3339), e.Level, e.Component, e.Message)...)
	}
	writeEntry("logs.txt", logsBuf)

	zw.Close()
}
