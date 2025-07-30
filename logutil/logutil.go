package logutil

import (
	"dnsloadbalancer/config"
	"io"
	"os"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// Buffer size for log messages
	logBufferSize = 1000
	// Timeout for flushing logs on shutdown
	flushTimeout = 5 * time.Second
)

var cfg = config.Get()

// LogMessage represents a buffered log message
type LogMessage struct {
	FormattedData []byte // Already formatted by logrus
}

// BufferedLogger wraps logrus with buffered output
// This logger uses a goroutine and buffered channel to prevent log operations
// from blocking the main application during high-volume logging or I/O congestion.
// Multiple calls to Flush() are safe and will ensure all buffered messages are written.
type BufferedLogger struct {
	*log.Logger
	logChan chan LogMessage
	done    chan struct{}
	wg      sync.WaitGroup
	once    sync.Once
	realOut io.Writer
	mu      sync.Mutex // protects closed state
	writeMu sync.Mutex // protects writes to realOut
	closed  bool
}

var Logger *BufferedLogger

// writeToOutput safely writes data to the output, ignoring errors since this is for logging
// This method is synchronized to prevent race conditions during concurrent writes
func (bl *BufferedLogger) writeToOutput(data []byte) {
	bl.writeMu.Lock()
	defer bl.writeMu.Unlock()

	if _, err := bl.realOut.Write(data); err != nil {
		// We can't really do much if writing logs fails, so we just ignore the error
		// This prevents errcheck warnings while maintaining the non-blocking nature
		_ = err
	}
}

// drainLogMessage processes a single log message and writes it to the output
func (bl *BufferedLogger) drainLogMessage(logMsg LogMessage) {
	// The data is already formatted by logrus, write it directly
	bl.writeToOutput(logMsg.FormattedData)
}

// bufferedWriter implements io.Writer and sends log messages to the buffer channel
type bufferedWriter struct {
	logger *BufferedLogger
}

func (bw *bufferedWriter) Write(p []byte) (n int, err error) {
	// Check if logger is closed
	bw.logger.mu.Lock()
	closed := bw.logger.closed
	bw.logger.mu.Unlock()

	if closed {
		// If logger is closed, write directly using synchronized method
		// The data is already formatted by logrus, so formatting is consistent
		bw.logger.writeToOutput(p)
		return len(p), nil
	}

	// Create a copy of the bytes to avoid potential data races
	// (the original slice might be reused by logrus)
	dataCopy := make([]byte, len(p))
	copy(dataCopy, p)

	// Send to buffer channel (non-blocking with select and default case)
	select {
	case bw.logger.logChan <- LogMessage{
		FormattedData: dataCopy,
	}:
		// Successfully buffered
	default:
		// If buffer is full, write directly using synchronized method to prevent blocking
		// This maintains consistent formatting (data is already formatted by logrus)
		// and prevents race conditions through synchronized writes
		bw.logger.writeToOutput(p)
	}

	return len(p), nil
}

// logWorker processes log messages from the buffer channel
func (bl *BufferedLogger) logWorker() {
	defer bl.wg.Done()

	for {
		select {
		case logMsg := <-bl.logChan:
			// Format and write the log message directly to the real output
			bl.drainLogMessage(logMsg)

		case <-bl.done:
			// Drain remaining messages before exiting
			for {
				select {
				case logMsg := <-bl.logChan:
					bl.drainLogMessage(logMsg)
				default:
					return
				}
			}
		}
	}
}

// Flush waits for all buffered log messages to be written
// This method can be called multiple times safely. If the logger is already
// closed, it will drain any remaining messages and return immediately.
func (bl *BufferedLogger) Flush() {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	// If already closed, just drain any remaining messages
	if bl.closed {
		bl.drainRemainingMessages()
		return
	}

	// Close the done channel to signal the worker to stop
	bl.once.Do(func() {
		close(bl.done)
	})
	bl.closed = true

	// Wait for the worker to finish with a timeout
	done := make(chan struct{})
	go func() {
		bl.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Worker finished gracefully
	case <-time.After(flushTimeout):
		// Timeout reached, force drain remaining messages
		bl.drainRemainingMessages()
	}
}

// drainRemainingMessages drains any messages left in the channel
func (bl *BufferedLogger) drainRemainingMessages() {
	for {
		select {
		case logMsg := <-bl.logChan:
			bl.drainLogMessage(logMsg)
		default:
			return
		}
	}
}

// NewBufferedLogger creates a new buffered logger
func NewBufferedLogger(output io.Writer) *BufferedLogger {
	baseLogger := log.New()

	bl := &BufferedLogger{
		Logger:  baseLogger,
		logChan: make(chan LogMessage, logBufferSize),
		done:    make(chan struct{}),
		realOut: output,
	}

	// Set up the buffered writer as the logger's output
	bl.Logger.SetOutput(&bufferedWriter{logger: bl})

	// Start the log worker goroutine
	bl.wg.Add(1)
	go bl.logWorker()

	return bl
}

// SetOutput sets the real output destination and formatter
func (bl *BufferedLogger) SetOutput(output io.Writer) {
	bl.realOut = output
}

// SetFormatter sets the log formatter for the underlying logrus logger
// Note: This only affects the formatting of messages before they reach our buffer.
// Messages are pre-formatted by logrus before being buffered.
func (bl *BufferedLogger) SetFormatter(formatter log.Formatter) {
	bl.Logger.SetFormatter(formatter)
}

func SetLogLevel(level string) {
	switch level {
	case "debug":
		Logger.SetLevel(log.DebugLevel)
	case "info":
		Logger.SetLevel(log.InfoLevel)
	case "warn":
		Logger.SetLevel(log.WarnLevel)
	case "error":
		Logger.SetLevel(log.ErrorLevel)
	case "fatal":
		Logger.SetLevel(log.FatalLevel)
	default:
		Logger.SetLevel(log.InfoLevel)
	}
}

func init() {
	Logger = NewBufferedLogger(os.Stdout)
	Logger.SetReportCaller(true)
	Logger.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
		DisableColors: true,
	})
	SetLogLevel(cfg.LogLevel)
}
