package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"kubedeploy/deploy-controller/internal/intake"
)

type Journal struct {
	Plan          intake.Plan `json:"plan"`
	State         string      `json:"state"`
	Reported      bool        `json:"reported"`
	Phase         string      `json:"phase,omitempty"`
	Reason        string      `json:"reason,omitempty"`
	LastError     string      `json:"last_error,omitempty"`
	Deadline      time.Time   `json:"deadline,omitempty"`
	PhaseDeadline time.Time   `json:"phase_deadline,omitempty"`
	Snapshot      Snapshot    `json:"snapshot,omitempty"`
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
	if j.State == "in_progress" && j.Reported {
		return nil, fmt.Errorf("invalid reported execution state")
	}
	switch j.State {
	case "in_progress", "success", "failure", "error":
	default:
		return nil, fmt.Errorf("invalid journal state")
	}
	if j.State == "in_progress" && j.Phase != "" {
		if j.Reported || j.Deadline.IsZero() {
			return nil, fmt.Errorf("invalid active journal")
		}
		switch j.Phase {
		case "applying":
		case "waiting", "smoke":
			if j.PhaseDeadline.IsZero() || j.PhaseDeadline.After(j.Deadline) {
				return nil, fmt.Errorf("invalid observation deadline")
			}
			if err := validateSnapshot(j.Plan, j.Snapshot); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("invalid execution phase")
		}
	}
	return &j, nil
}

func (s *Store) Save(j Journal) error {
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return s.writeAtomic("deployment.json", raw)
}

func (s *Store) SaveDiagnostics(id int64, raw []byte) error {
	if id <= 0 || !json.Valid(raw) {
		return fmt.Errorf("invalid diagnostics")
	}
	return s.writeAtomic(fmt.Sprintf("diagnostics-%d.json", id), raw)
}

func (s *Store) writeAtomic(name string, raw []byte) error {
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
	if err = os.Rename(f.Name(), filepath.Join(s.dir, name)); err != nil {
		return err
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
