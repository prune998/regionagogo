package main

import "github.com/prometheus/client_golang/prometheus"

var (
	promQueryProcessedDelay = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "regionagogo_query_processed_delay",
		Help: "histogram of delays for processing a request",
	})
)

func init() {
	prometheus.MustRegister(promQueryProcessedDelay)
}
