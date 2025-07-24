package logutil

import (
	"dnsloadbalancer/config"
	"os"

	log "github.com/sirupsen/logrus"
)

var Logger *log.Logger

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
	Logger = log.New()
	Logger.SetOutput(os.Stdout)
	Logger.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
		DisableColors: true,
	})
	SetLogLevel(config.LogLevel)
}
