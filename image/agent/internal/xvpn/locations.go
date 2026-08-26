package xvpn

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Location — элемент списка /v1/locations.
type Location struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
	City    string `json:"city"`
	// Smart: рекомендованная демоном локация ("умный" выбор).
	Smart bool `json:"smart"`
}

// maxCacheAge — спека expressvpn-control: кеш обновляется не реже раза в 24 ч.
const maxCacheAge = 24 * time.Hour

type locationsFile struct {
	UpdatedAt time.Time  `json:"updatedAt"`
	Locations []Location `json:"locations"`
}

// LocationCache — кеш списка локаций в /data/locations.json.
type LocationCache struct {
	mu   sync.Mutex
	path string
	cli  *CLI
	data locationsFile
}

func NewLocationCache(dataDir string, cli *CLI) *LocationCache {
	c := &LocationCache{path: filepath.Join(dataDir, "locations.json"), cli: cli}
	if raw, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(raw, &c.data)
	}
	return c
}

func (c *LocationCache) List() []Location {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Location, len(c.data.Locations))
	copy(out, c.data.Locations)
	return out
}

func (c *LocationCache) Stale() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.data.UpdatedAt) > maxCacheAge
}

// Refresh запрашивает у демона список регионов и smart-регион и сохраняет кеш.
func (c *LocationCache) Refresh(ctx context.Context) error {
	regions, err := c.cli.Regions(ctx)
	if err != nil {
		return err
	}
	smart, _ := c.cli.Smart(ctx)

	locs := make([]Location, 0, len(regions))
	for _, id := range regions {
		loc := locationFromID(id)
		loc.Smart = id == "smart" || (smart != "" && id == smart)
		locs = append(locs, loc)
	}
	c.mu.Lock()
	c.data = locationsFile{UpdatedAt: time.Now().UTC(), Locations: locs}
	raw, err := json.MarshalIndent(c.data, "", "  ")
	path := c.path
	c.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// locationFromID строит человекочитаемые поля из id региона
// (например "germany-frankfurt-1" → Germany / Frankfurt 1).
func locationFromID(id string) Location {
	if id == "smart" {
		return Location{ID: "smart", Name: "Smart location", Country: "", City: ""}
	}
	parts := strings.Split(id, "-")
	title := func(ss []string) string {
		for i, s := range ss {
			if s != "" {
				ss[i] = strings.ToUpper(s[:1]) + s[1:]
			}
		}
		return strings.Join(ss, " ")
	}
	loc := Location{ID: id}
	if len(parts) == 1 {
		loc.Country = title(parts[:1])
	} else {
		loc.Country = title(parts[:1])
		loc.City = title(parts[1:])
	}
	loc.Name = strings.TrimSpace(loc.Country + " — " + loc.City)
	loc.Name = strings.TrimSuffix(loc.Name, " — ")
	return loc
}
