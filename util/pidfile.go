package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// CreatePidFile creates a PID file with the current process ID
func CreatePidFile(pidPath string) error {
	if pidPath == "" {
		return fmt.Errorf("PID file path cannot be empty")
	}

	// Create directory if it doesn't exist
	dir := filepath.Dir(pidPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create PID file directory %s: %v", dir, err)
	}

	// Check if PID file already exists and if the process is running
	if err := checkExistingPidFile(pidPath); err != nil {
		return err
	}

	// Write current process PID to file atomically using O_EXCL to prevent race conditions
	pid := os.Getpid()
	pidStr := strconv.Itoa(pid)

	// Try to create the file exclusively (will fail if file exists)
	file, err := os.OpenFile(pidPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			// File was created by another process between our check and create
			// Check again if that process is still running
			if checkErr := checkExistingPidFile(pidPath); checkErr != nil {
				return checkErr
			}
			// Try one more time
			file, err = os.OpenFile(pidPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if err != nil {
				return fmt.Errorf("failed to create PID file %s: %v", pidPath, err)
			}
		} else {
			return fmt.Errorf("failed to create PID file %s: %v", pidPath, err)
		}
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			// Log or handle close error, but don't override the write error
			err = fmt.Errorf("failed to close PID file %s: %v", pidPath, closeErr)
		}
	}()

	if _, err := file.WriteString(pidStr); err != nil {
		return fmt.Errorf("failed to write PID to file %s: %v", pidPath, err)
	}

	return nil
}

// RemovePidFile removes the PID file
func RemovePidFile(pidPath string) error {
	if pidPath == "" {
		return nil // Nothing to do if no PID file specified
	}

	if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove PID file %s: %v", pidPath, err)
	}

	return nil
}

// checkExistingPidFile checks if a PID file exists and if the process is still running
func checkExistingPidFile(pidPath string) error {
	data, err := os.ReadFile(pidPath)
	if os.IsNotExist(err) {
		// No existing PID file, safe to proceed
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read existing PID file %s: %v", pidPath, err)
	}

	// Parse PID from file
	pidStr := strings.TrimSpace(string(data))
	if pidStr == "" {
		// Empty PID file, remove it and continue
		_ = os.Remove(pidPath) // Ignore error if file doesn't exist
		return nil
	}

	existingPid, err := strconv.Atoi(pidStr)
	if err != nil {
		// Invalid PID in file, remove it and continue
		_ = os.Remove(pidPath) // Ignore error if file doesn't exist
		return nil
	}

	// Check if process is still running
	if isProcessRunning(existingPid) {
		return fmt.Errorf("DNS forwarder is already running with PID %d (PID file: %s)", existingPid, pidPath)
	}

	// Process not running, remove stale PID file
	_ = os.Remove(pidPath) // Ignore error if file doesn't exist
	return nil
}

// isProcessRunning checks if a process with the given PID is running
func isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}

	// Send signal 0 to check if process exists
	// This doesn't actually send a signal, just checks if we can send one
	err := syscall.Kill(pid, 0)
	return err == nil
}

// ReadPidFile reads and returns the PID from a PID file
func ReadPidFile(pidPath string) (int, error) {
	if pidPath == "" {
		return 0, fmt.Errorf("PID file path cannot be empty")
	}

	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read PID file %s: %v", pidPath, err)
	}

	pidStr := strings.TrimSpace(string(data))
	if pidStr == "" {
		return 0, fmt.Errorf("PID file %s is empty", pidPath)
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("invalid PID in file %s: %s", pidPath, pidStr)
	}

	return pid, nil
}
