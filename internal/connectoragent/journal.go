package connectoragent

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const journalLimit = 10000

type journalEntry struct {
	ID        string    `json:"id"`
	Deadline  time.Time `json:"deadline"`
	Container string    `json:"container,omitempty"`
}
type journal struct {
	dir     string
	lock    *os.File
	entries map[string]journalEntry
}

func ownedByCurrentUser(s os.FileInfo) bool {
	v, ok := s.Sys().(*syscall.Stat_t)
	return ok && v.Uid == uint32(os.Getuid())
}
func openPrivate(path string, flags int, mode os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(mode))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
func openJournal(dir string) (*journal, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, errors.New("invalid state_dir")
	}
	// Parent must already exist; do not follow symlinks through MkdirAll.
	if err := safePath(filepath.Dir(dir)); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, errors.New("cannot create state_dir")
	}
	if err := safePath(dir); err != nil {
		return nil, err
	}
	s, err := os.Stat(dir)
	if err != nil || !s.IsDir() || s.Mode().Perm() != 0700 || !ownedByCurrentUser(s) {
		return nil, errors.New("state_dir must be owned by the Connector user with mode 0700")
	}
	f, err := openPrivate(filepath.Join(dir, "lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	s, err = f.Stat()
	if err != nil || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || !ownedByCurrentUser(s) {
		f.Close()
		return nil, errors.New("invalid state lock")
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, errors.New("another Connector already uses state_dir")
	}
	j := &journal{dir: dir, lock: f, entries: map[string]journalEntry{}}
	jf, err := openPrivate(filepath.Join(dir, "journal.jsonl"), os.O_RDONLY, 0)
	if os.IsNotExist(err) {
		return j, nil
	}
	if err != nil {
		j.Close()
		return nil, errors.New("cannot open execution journal")
	}
	defer jf.Close()
	s, err = jf.Stat()
	if err != nil || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || !ownedByCurrentUser(s) || s.Size() > 8<<20 {
		j.Close()
		return nil, errors.New("invalid execution journal")
	}
	scanner := bufio.NewScanner(jf)
	scanner.Buffer(make([]byte, 4096), 4096)
	for scanner.Scan() {
		var e journalEntry
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.ID == "" || len(e.ID) > 128 || e.Deadline.IsZero() || len(j.entries) >= journalLimit || (e.Container != "" && e.Container != containerName(e.ID)) {
			j.Close()
			return nil, errors.New("execution journal is corrupt or full")
		}
		j.entries[e.ID] = e
	}
	if scanner.Err() != nil {
		j.Close()
		return nil, errors.New("execution journal is corrupt")
	}
	return j, nil
}
func (j *journal) Close() {
	if j.lock != nil {
		syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
		j.lock.Close()
		j.lock = nil
	}
}
func containerName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "rillgate-" + hex.EncodeToString(sum[:16])
}
func (j *journal) Record(e journalEntry) error {
	if _, ok := j.entries[e.ID]; ok {
		return errors.New("job was already attempted; replay is prohibited")
	}
	retained := make(map[string]journalEntry, len(j.entries)+1)
	for id, entry := range j.entries {
		if entry.Deadline.After(time.Now().Add(-24 * time.Hour)) {
			retained[id] = entry
		}
	}
	if len(retained) >= journalLimit {
		return errors.New("execution journal capacity reached")
	}
	retained[e.ID] = e
	tmp, err := openPrivate(filepath.Join(j.dir, "journal.pending"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return errors.New("unfinished journal write; inspect state before restart")
	}
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmp.Name())
		}
	}()
	encoder := json.NewEncoder(tmp)
	for _, entry := range retained {
		if err := encoder.Encode(entry); err != nil {
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(j.dir, "journal.jsonl")); err != nil {
		return err
	}
	d, err := os.Open(j.dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return err
	}
	ok = true
	j.entries = retained
	return nil
}
