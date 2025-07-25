package pidfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"dnsloadbalancer/logutil"
)

// PIDFile represents a PID file manager
type PIDFile struct {
	path string
}

// New creates a new PIDFile instance
func New(path string) *PIDFile {
	return &PIDFile{path: path}
}

// Write writes the current process PID to the file
func (p *PIDFile) Write() error {
	// Ensure the directory exists
	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create PID file directory %s: %v", dir, err)
	}

	// Check if PID file already exists and if the process is still running
	if p.exists() {
		if p.isProcessRunning() {
			return fmt.Errorf("PID file %s exists and process is still running", p.path)
		}
		logutil.Logger.Warnf("Stale PID file %s found, removing it", p.path)
		if err := p.Remove(); err != nil {
			logutil.Logger.Errorf("Failed to remove stale PID file: %v", err)
		}
	}

	// Write current PID to file
	pid := os.Getpid()
	content := strconv.Itoa(pid)

	if err := os.WriteFile(p.path, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write PID file %s: %v", p.path, err)
	}

	logutil.Logger.Infof("PID file created: %s (PID: %d)", p.path, pid)
	return nil
}

// Remove removes the PID file
func (p *PIDFile) Remove() error {
	if !p.exists() {
		return nil
	}

	if err := os.Remove(p.path); err != nil {
		return fmt.Errorf("failed to remove PID file %s: %v", p.path, err)
	}

	logutil.Logger.Infof("PID file removed: %s", p.path)
	return nil
}

// exists checks if the PID file exists
func (p *PIDFile) exists() bool {
	_, err := os.Stat(p.path)
	return !os.IsNotExist(err)
}

// readPID reads the PID from the file
func (p *PIDFile) readPID() (int, error) {
	content, err := os.ReadFile(p.path)
	if err != nil {
		return 0, err
	}

	pid, err := strconv.Atoi(string(content))
	if err != nil {
		return 0, fmt.Errorf("invalid PID in file %s: %v", p.path, err)
	}

	return pid, nil
}

// isProcessRunning checks if the process with the PID in the file is still running
func (p *PIDFile) isProcessRunning() bool {
	pid, err := p.readPID()
	if err != nil {
		return false
	}

	// Check if process exists by sending signal 0
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// GetPID returns the PID from the file
func (p *PIDFile) GetPID() (int, error) {
	if !p.exists() {
		return 0, fmt.Errorf("PID file %s does not exist", p.path)
	}
	return p.readPID()
}
