package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"kubedeploy/deploy-controller/internal/intake"
)

type Journal struct {
	Plan     intake.Plan `json:"plan"`
	State    string      `json:"state"`
	Reported bool        `json:"reported"`
}

type Store struct {
	dir  string
	lock *os.File
}

func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("execution requires --state-dir")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(abs, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state directory is already locked: %w", err)
	}
	return &Store{dir: abs, lock: f}, nil
}

func (s *Store) Close() error { return s.lock.Close() }

func (s *Store) Load() (*Journal, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, "deployment.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if err = json.Unmarshal(raw, &j); err != nil {
		return nil, err
	}
	if j.Plan.DeploymentID <= 0 {
		return nil, fmt.Errorf("invalid journal deployment")
	}
	switch j.State {
	case "in_progress", "success", "failure", "error":
	default:
		return nil, fmt.Errorf("invalid journal state")
	}
	return &j, nil
}

func (s *Store) Save(j Journal) error {
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".deployment-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), filepath.Join(s.dir, "deployment.json")); err != nil {
		return err
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
