package xvpn

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Демон ExpressVPN хранит настройки и сессию входа в /opt/expressvpn/etc
// (docs/spikes/S1.md). Чтобы они переживали пересоздание контейнера, каталог
// подменяется симлинком на <dataDir>/expressvpn; при первом старте содержимое
// из образа переносится в том как начальное состояние.
const (
	etcPath = "/opt/expressvpn/etc"
)

// EnsureStateDir идемпотентно переключает /opt/expressvpn/etc на том.
func EnsureStateDir(dataDir string) error {
	target := filepath.Join(dataDir, "expressvpn")

	fi, err := os.Lstat(etcPath)
	if os.IsNotExist(err) {
		// Обычный первый старт: инсталлятор каталог не создаёт — его создал бы
		// демон при первом запуске. Сразу подставляем симлинк на том.
		if _, err := os.Stat(filepath.Dir(etcPath)); os.IsNotExist(err) {
			// Вне образа (юнит-тесты) — ничего не делаем.
			return nil
		}
		if err := os.MkdirAll(target, 0o700); err != nil {
			return err
		}
		return os.Symlink(target, etcPath)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		// Уже переключено (перезапуск контейнера).
		return os.MkdirAll(target, 0o700)
	}

	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	empty, err := isEmptyDir(target)
	if err != nil {
		return err
	}
	if empty {
		if err := copyTree(etcPath, target); err != nil {
			return fmt.Errorf("seed %s from %s: %w", target, etcPath, err)
		}
	}
	if err := os.RemoveAll(etcPath); err != nil {
		return err
	}
	return os.Symlink(target, etcPath)
}

func isEmptyDir(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); err == io.EOF {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return false, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if rel == "." {
				return nil
			}
			return os.MkdirAll(out, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, out)
		default:
			return copyFile(path, out, info.Mode().Perm())
		}
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
