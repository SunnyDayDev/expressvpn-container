package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Коды capability из include/uapi/linux/capability.h.
const (
	capNetAdmin  = 12
	capSysPtrace = 19
)

// exitPrereqFailed — EX_CONFIG: контейнер запущен без обязательных прав.
const exitPrereqFailed = 78

type prereqError struct{ msg string }

func (e *prereqError) Error() string { return e.msg }

// checkPrereqs проверяет, что контейнер запущен с /dev/net/tun и нужными
// capabilities. SYS_PTRACE обязателен: без него демон ExpressVPN отвергает
// IPC-клиентов и все команды expressvpnctl таймаутятся (docs/spikes/S1.md).
func checkPrereqs() error {
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return &prereqError{"missing /dev/net/tun (add --device /dev/net/tun)"}
	}
	caps, err := effectiveCaps()
	if err != nil {
		return fmt.Errorf("read effective capabilities: %w", err)
	}
	if caps&(1<<capNetAdmin) == 0 {
		return &prereqError{"missing capability NET_ADMIN (add --cap-add NET_ADMIN)"}
	}
	if caps&(1<<capSysPtrace) == 0 {
		return &prereqError{"missing capability SYS_PTRACE (add --cap-add SYS_PTRACE; required for expressvpnctl IPC)"}
	}
	return nil
}

// effectiveCaps читает битовую маску CapEff из /proc/self/status.
func effectiveCaps() (uint64, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "CapEff:"); ok {
			return strconv.ParseUint(strings.TrimSpace(rest), 16, 64)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("CapEff not found in /proc/self/status")
}
