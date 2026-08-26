package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestApplyMergePatch_AppliesAndPersists(t *testing.T) {
	s := newTestStore(t)
	applied, err := s.ApplyMergePatch([]byte(`{"expressvpn":{"location":"de-frankfurt-1","protocol":"lightway_tcp"}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"expressvpn.location", "expressvpn.protocol"}
	if len(applied) != 2 || applied[0] != want[0] || applied[1] != want[1] {
		t.Fatalf("applied=%v want %v", applied, want)
	}
	if got := s.Get().ExpressVPN.Location; got != "de-frankfurt-1" {
		t.Fatalf("location=%q", got)
	}

	// Файл появился, права 0600, содержимое парсится повторно.
	fi, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%v want 0600", fi.Mode().Perm())
	}
	s2, err := NewStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Get().ExpressVPN.Protocol != "lightway_tcp" {
		t.Fatal("config not persisted")
	}
}

func TestApplyMergePatch_InvalidRejectedAtomically(t *testing.T) {
	s := newTestStore(t)
	// Корректный protocol + некорректный uplink.mode: не применяется ничего.
	_, err := s.ApplyMergePatch([]byte(`{"expressvpn":{"protocol":"lightway_tcp"},"uplink":{"mode":"wireguard"}}`))
	if err == nil {
		t.Fatal("want error")
	}
	var fe *FieldError
	if !asFieldError(err, &fe) || fe.Field != "uplink.mode" {
		t.Fatalf("err=%v", err)
	}
	if got := s.Get().ExpressVPN.Protocol; got != "auto" {
		t.Fatalf("protocol=%q, partial apply happened", got)
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Fatal("file written despite rejected patch")
	}
}

func TestApplyMergePatch_UnknownFieldRejected(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ApplyMergePatch([]byte(`{"uplink":{"mode":"host","bogus":1}}`)); err == nil {
		t.Fatal("want error for unknown field")
	}
}

func TestApplyMergePatch_SocksRequiresHostPort(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ApplyMergePatch([]byte(`{"uplink":{"mode":"socks5"}}`)); err == nil {
		t.Fatal("want error: socks5 without host")
	}
	if _, err := s.ApplyMergePatch([]byte(`{"uplink":{"mode":"socks5","socks5":{"host":"host.docker.internal","port":1086}}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestPasswordMaskedAndMaskRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ApplyMergePatch([]byte(`{"uplink":{"mode":"socks5","socks5":{"host":"h","port":1,"password":"secret1"}}}`)); err != nil {
		t.Fatal(err)
	}
	red := Redacted(s.Get())
	raw, _ := json.Marshal(red)
	if strings.Contains(string(raw), "secret1") {
		t.Fatal("secret leaked in redacted config")
	}
	if red.Uplink.Socks5.Password != "***" {
		t.Fatalf("password=%q want ***", red.Uplink.Socks5.Password)
	}

	// Клиент отправляет назад маскированный объект — пароль не затирается.
	rawRed, _ := json.Marshal(red)
	if _, err := s.ApplyMergePatch(rawRed); err != nil {
		t.Fatal(err)
	}
	if got := s.Get().Uplink.Socks5.Password; got != "secret1" {
		t.Fatalf("password=%q, mask overwrote secret", got)
	}
}

func TestProxyAuthNullDisables(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ApplyMergePatch([]byte(`{"proxy":{"auth":{"username":"u","password":"p"}}}`)); err != nil {
		t.Fatal(err)
	}
	if s.Get().Proxy.Auth == nil {
		t.Fatal("auth not set")
	}
	if _, err := s.ApplyMergePatch([]byte(`{"proxy":{"auth":null}}`)); err != nil {
		t.Fatal(err)
	}
	if s.Get().Proxy.Auth != nil {
		t.Fatal("auth not cleared by null")
	}
}

func asFieldError(err error, target **FieldError) bool {
	fe, ok := err.(*FieldError)
	if ok {
		*target = fe
	}
	return ok
}
