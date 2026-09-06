package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	mu   sync.Mutex
	file *os.File
	path string
)

func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return path
}

func DefaultPath() string {
	if p := os.Getenv("GITLIFT_LOG"); p != "" {
		return p
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		if home, err := os.UserHomeDir(); err == nil {
			state = filepath.Join(home, ".local", "state")
		} else {
			state = os.TempDir()
		}
	}
	return filepath.Join(state, "gitlift", "gitlift.log")
}

func Open(p string) error {
	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	file, path = f, p
	return nil
}

func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Close()
		file = nil
	}
}

func Printf(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if file == nil {
		return
	}
	fmt.Fprintf(file, "%s  %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func Err(context string, err error) {
	if err == nil {
		return
	}
	Printf("%-28s %v", context, err)
}
