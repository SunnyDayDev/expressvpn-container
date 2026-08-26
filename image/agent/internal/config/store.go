package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Store — конфигурация в /data/config.json: чтение при старте, атомарная
// запись (tmp + rename, 0600), применение JSON merge patch (RFC 7386)
// «всё или ничего».
type Store struct {
	mu   sync.Mutex
	path string
	cur  Config
	// onChange вызывается после успешного применения патча (будит реконсайлер).
	onChange func(Config)
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, cur: Default()}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// Свежий том — дефолты, файл появится при первом изменении.
	case err != nil:
		return nil, err
	default:
		dec := json.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(&s.cur); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if err := Validate(s.cur); err != nil {
		return nil, fmt.Errorf("stored config invalid: %w", err)
	}
	return s, nil
}

func (s *Store) OnChange(f func(Config)) {
	s.mu.Lock()
	s.onChange = f
	s.mu.Unlock()
}

func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// ApplyMergePatch применяет частичный объект к текущей конфигурации,
// валидирует результат целиком и сохраняет атомарно. Возвращает список
// применённых (изменившихся) ключей.
func (s *Store) ApplyMergePatch(patch []byte) ([]string, error) {
	var probe any
	if err := json.Unmarshal(patch, &probe); err != nil {
		return nil, &FieldError{Field: "", Msg: "body must be a JSON object"}
	}
	if _, ok := probe.(map[string]any); !ok {
		return nil, &FieldError{Field: "", Msg: "body must be a JSON object"}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	curRaw, err := json.Marshal(s.cur)
	if err != nil {
		return nil, err
	}
	merged := mergePatch(mustMap(curRaw), probe.(map[string]any), s.cur)

	mergedRaw, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	var next Config
	dec := json.NewDecoder(bytes.NewReader(mergedRaw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return nil, &FieldError{Field: "", Msg: "unknown or mistyped field: " + err.Error()}
	}
	if err := Validate(next); err != nil {
		return nil, err
	}

	applied := diffKeys("", mustMap(curRaw), merged)
	sort.Strings(applied)
	if len(applied) == 0 {
		return applied, nil
	}

	if err := s.save(next); err != nil {
		return nil, err
	}
	s.cur = next
	if s.onChange != nil {
		go s.onChange(next)
	}
	return applied, nil
}

// save пишет конфигурацию атомарно с правами 0600.
func (s *Store) save(c Config) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// mergePatch — RFC 7386 поверх map-представления. Значение "***" для полей
// паролей означает «не менять» (так GET /v1/config маскирует секреты, и
// клиент может безопасно отправить объект обратно).
func mergePatch(target map[string]any, patch map[string]any, cur Config) map[string]any {
	out := make(map[string]any, len(target))
	for k, v := range target {
		out[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(out, k)
			continue
		}
		if pm, ok := v.(map[string]any); ok {
			tm, _ := out[k].(map[string]any)
			if tm == nil {
				tm = map[string]any{}
			}
			out[k] = mergePatch(tm, pm, cur)
			continue
		}
		if vs, ok := v.(string); ok && vs == mask && isPasswordField(k) {
			continue
		}
		out[k] = v
	}
	return out
}

func isPasswordField(key string) bool { return key == "password" }

func mustMap(raw []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(err)
	}
	return m
}

// diffKeys возвращает листовые ключи, отличающиеся между a и b.
func diffKeys(prefix string, a, b map[string]any) []string {
	var out []string
	keys := map[string]struct{}{}
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	for k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		av, aok := a[k]
		bv, bok := b[k]
		am, amap := av.(map[string]any)
		bm, bmap := bv.(map[string]any)
		switch {
		case amap && bmap:
			out = append(out, diffKeys(path, am, bm)...)
		case aok != bok || !jsonEqual(av, bv):
			out = append(out, path)
		}
	}
	return out
}

func jsonEqual(a, b any) bool {
	ar, _ := json.Marshal(a)
	br, _ := json.Marshal(b)
	return bytes.Equal(ar, br)
}
