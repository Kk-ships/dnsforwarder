package main

import (
	"dnsloadbalancer/metric"
)

var metricsRecorder = metric.MetricsRecorderInstance
var StartMetricsUpdater = metric.StartMetricsUpdater
var StartMetricsServer = metric.StartMetricsServer
