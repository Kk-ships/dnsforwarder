package logutil

import (
	"os"

	log "github.com/sirupsen/logrus"
)

var Logger *log.Logger

func init() {
	Logger = log.New()
	Logger.SetOutput(os.Stdout)
	Logger.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
		DisableColors: true,
	})
	Logger.SetLevel(log.WarnLevel)
}
