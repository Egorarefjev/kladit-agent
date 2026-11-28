package worker

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
)

type journal struct {
	logQueue   []string
	logIndexed bool
	root       *os.Root
	lock       *os.File
	state      journalState
}

func openJournal(path string) (*journal, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
		return nil, fmt.Errorf("worker state requires a private absolute directory")
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create worker state directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("worker state directory must be owned by this user with mode 0700")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open worker state directory")
	}
	j := &journal{root: root}
	_, priorLock := root.Lstat(".lock")
	lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("open worker lock")
	}
	j.lock = lock
	if err = privateFile(lock); err == nil {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		j.Close()
		return nil, fmt.Errorf("worker lock unavailable")
	}
	if err = j.read("state.json", &j.state); errors.Is(err, os.ErrNotExist) {
		if priorLock == nil {
			j.Close()
			return nil, fmt.Errorf("worker state missing; reconciliation required")
		}
		generation := make([]byte, 16)
		if _, err = rand.Read(generation); err != nil {
			j.Close()
			return nil, err
		}
		generation[6] = (generation[6] & 15) | 64
		generation[8] = (generation[8] & 63) | 128
		j.state = journalState{Version: 1, Generation: fmt.Sprintf("%x-%x-%x-%x-%x", generation[:4], generation[4:6], generation[6:8], generation[8:10], generation[10:])}
		err = j.save()
	}
	if err != nil || j.state.Version != 1 || !connection.ValidJobIdentity(j.state.Generation) {
		j.Close()
		return nil, fmt.Errorf("invalid worker journal; reconciliation required")
	}
	if j.state.Current != nil && !validRecord(j.state.Current, j.state.Generation) {
		j.Close()
		return nil, fmt.Errorf("unknown worker state; reconciliation required")
	}
	return j, nil
}

func privateFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("worker journal file must be private")
	}
	return nil
}

func (j *journal) read(name string, value any) error {
	f, err := j.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = privateFile(f); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return fmt.Errorf("invalid worker journal size")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid worker journal data")
	}
	return nil
}

func (j *journal) write(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 65536 {
		return fmt.Errorf("worker journal exceeds its bound")
	}
	suffix := make([]byte, 12)
	if _, err = rand.Read(suffix); err != nil {
		return err
	}
	temp := fmt.Sprintf(".state-%x.tmp", suffix)
	f, err := j.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return fmt.Errorf("create worker journal temporary file")
	}
	defer j.root.Remove(temp) // Only an unreferenced, privately created temporary file.
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return fmt.Errorf("persist worker journal")
	}
	if err = j.root.Rename(temp, name); err != nil {
		return fmt.Errorf("commit worker journal")
	}
	dir, err := j.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (j *journal) save() error  { return j.write("state.json", j.state) }
func (j *journal) Close() error { return errors.Join(j.lock.Close(), j.root.Close()) }

func (j *journal) checkHistoryBound() error {
	directory, err := j.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(1028)
	if err != nil && err != io.EOF {
		return err
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".done-") {
			count++
		}
	}
	if len(entries) >= 1028 || count >= 1024 {
		return fmt.Errorf("worker history limit reached; generation retirement required")
	}
	return nil
}
