package logutil

import (
	"fmt"
	"log"
	"sync"
)

type LogRingBuffer struct {
	entries []string
	max     int
	idx     int
	full    bool
	sync.Mutex
}

func NewLogRingBuffer(size int) *LogRingBuffer {
	return &LogRingBuffer{
		entries: make([]string, size),
		max:     size,
	}
}

func (l *LogRingBuffer) Add(entry string) {
	l.Lock()
	defer l.Unlock()
	l.entries[l.idx] = entry
	l.idx = (l.idx + 1) % l.max
	if l.idx == 0 {
		l.full = true
	}
}

func (l *LogRingBuffer) GetAll() []string {
	l.Lock()
	defer l.Unlock()
	if !l.full {
		return l.entries[:l.idx]
	}
	result := make([]string, l.max)
	copy(result, l.entries[l.idx:])
	copy(result[l.max-l.idx:], l.entries[:l.idx])
	return result
}

var LogBuffer = NewLogRingBuffer(500)

func LogWithBufferf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	LogBuffer.Add(msg)
	log.Printf(format, v...)
}

func LogWithBufferFatalf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	LogBuffer.Add(msg)
	log.Fatalf(format, v...)
}
