package util

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestCreatePidFile(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "test.pid")

	// Test creating PID file
	err := CreatePidFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to create PID file: %v", err)
	}

	// Verify PID file exists and contains correct PID
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to read PID file: %v", err)
	}

	expectedPid := strconv.Itoa(os.Getpid())
	actualPid := string(data)
	if actualPid != expectedPid {
		t.Errorf("PID file contains %s, expected %s", actualPid, expectedPid)
	}
}

func TestCreatePidFileEmptyPath(t *testing.T) {
	err := CreatePidFile("")
	if err == nil {
		t.Error("Expected error for empty PID file path")
	}
}

func TestCreatePidFileExistingRunningProcess(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "test.pid")

	// Create PID file with current process PID
	currentPid := os.Getpid()
	err := os.WriteFile(pidPath, []byte(strconv.Itoa(currentPid)), 0644)
	if err != nil {
		t.Fatalf("Failed to create test PID file: %v", err)
	}

	// Try to create PID file again - should fail
	err = CreatePidFile(pidPath)
	if err == nil {
		t.Error("Expected error when PID file exists with running process")
	}
}

func TestCreatePidFileStaleProcess(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "test.pid")

	// Create PID file with non-existent PID (assuming 99999 doesn't exist)
	stalePid := 99999
	err := os.WriteFile(pidPath, []byte(strconv.Itoa(stalePid)), 0644)
	if err != nil {
		t.Fatalf("Failed to create test PID file: %v", err)
	}

	// Try to create PID file - should succeed by removing stale file
	err = CreatePidFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to create PID file after removing stale file: %v", err)
	}

	// Verify new PID file contains current process PID
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to read new PID file: %v", err)
	}

	expectedPid := strconv.Itoa(os.Getpid())
	actualPid := string(data)
	if actualPid != expectedPid {
		t.Errorf("New PID file contains %s, expected %s", actualPid, expectedPid)
	}
}

func TestRemovePidFile(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "test.pid")

	// Create PID file
	err := CreatePidFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to create PID file: %v", err)
	}

	// Remove PID file
	err = RemovePidFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to remove PID file: %v", err)
	}

	// Verify PID file is gone
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("PID file still exists after removal")
	}
}

func TestRemovePidFileEmptyPath(t *testing.T) {
	// Should not error with empty path
	err := RemovePidFile("")
	if err != nil {
		t.Errorf("Unexpected error for empty PID file path: %v", err)
	}
}

func TestRemovePidFileNonExistent(t *testing.T) {
	// Should not error when removing non-existent file
	err := RemovePidFile("/tmp/non-existent.pid")
	if err != nil {
		t.Errorf("Unexpected error for non-existent PID file: %v", err)
	}
}

func TestReadPidFile(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "test.pid")

	// Create PID file
	expectedPid := os.Getpid()
	err := CreatePidFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to create PID file: %v", err)
	}

	// Read PID file
	actualPid, err := ReadPidFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to read PID file: %v", err)
	}

	if actualPid != expectedPid {
		t.Errorf("Read PID %d, expected %d", actualPid, expectedPid)
	}
}

func TestReadPidFileEmptyPath(t *testing.T) {
	_, err := ReadPidFile("")
	if err == nil {
		t.Error("Expected error for empty PID file path")
	}
}

func TestReadPidFileNonExistent(t *testing.T) {
	_, err := ReadPidFile("/tmp/non-existent.pid")
	if err == nil {
		t.Error("Expected error for non-existent PID file")
	}
}

func TestIsProcessRunning(t *testing.T) {
	// Test with current process (should be running)
	currentPid := os.Getpid()
	if !isProcessRunning(currentPid) {
		t.Error("Current process should be reported as running")
	}

	// Test with invalid PID
	if isProcessRunning(-1) {
		t.Error("Invalid PID should be reported as not running")
	}

	if isProcessRunning(0) {
		t.Error("PID 0 should be reported as not running")
	}

	// Test with likely non-existent PID
	if isProcessRunning(99999) {
		t.Error("Non-existent PID 99999 should be reported as not running")
	}
}

func TestCreatePidFileCreateDirectory(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	subDir := filepath.Join(tempDir, "subdir", "nested")
	pidPath := filepath.Join(subDir, "test.pid")

	// Directory doesn't exist yet, should be created
	err := CreatePidFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to create PID file with new directory: %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(subDir); os.IsNotExist(err) {
		t.Error("Directory was not created")
	}

	// Verify PID file exists
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Error("PID file was not created")
	}
}

func TestCreatePidFileCorruptedFile(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "test.pid")

	// Create corrupted PID file (non-numeric content)
	err := os.WriteFile(pidPath, []byte("not-a-number"), 0644)
	if err != nil {
		t.Fatalf("Failed to create corrupted PID file: %v", err)
	}

	// Should succeed by removing corrupted file
	err = CreatePidFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to create PID file after removing corrupted file: %v", err)
	}

	// Verify new PID file contains current process PID
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to read new PID file: %v", err)
	}

	expectedPid := strconv.Itoa(os.Getpid())
	actualPid := string(data)
	if actualPid != expectedPid {
		t.Errorf("New PID file contains %s, expected %s", actualPid, expectedPid)
	}
}

func TestCreatePidFileEmptyFile(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "test.pid")

	// Create empty PID file
	err := os.WriteFile(pidPath, []byte(""), 0644)
	if err != nil {
		t.Fatalf("Failed to create empty PID file: %v", err)
	}

	// Should succeed by removing empty file
	err = CreatePidFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to create PID file after removing empty file: %v", err)
	}

	// Verify new PID file contains current process PID
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to read new PID file: %v", err)
	}

	expectedPid := strconv.Itoa(os.Getpid())
	actualPid := string(data)
	if actualPid != expectedPid {
		t.Errorf("New PID file contains %s, expected %s", actualPid, expectedPid)
	}
}

// TestConcurrentPidFileAccess tests concurrent access to PID file creation
func TestConcurrentPidFileAccess(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "concurrent.pid")

	// Start multiple goroutines trying to create the same PID file
	results := make(chan error, 3)

	for i := 0; i < 3; i++ {
		go func() {
			results <- CreatePidFile(pidPath)
		}()
	}

	// First one should succeed, others should fail
	var successCount, errorCount int
	for i := 0; i < 3; i++ {
		err := <-results
		if err == nil {
			successCount++
		} else {
			errorCount++
		}
	}

	if successCount != 1 {
		t.Errorf("Expected exactly 1 success, got %d", successCount)
	}
	if errorCount != 2 {
		t.Errorf("Expected exactly 2 errors, got %d", errorCount)
	}

	// Clean up
	_ = RemovePidFile(pidPath) // Ignore cleanup error in test
}
