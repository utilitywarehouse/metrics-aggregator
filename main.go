package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

var (
	log *slog.Logger

	pcDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "metrics_aggregation_duration_seconds",
		Help: "Duration of a collection",
	},
		[]string{"remote"},
	)
)

type RemoteAggregator struct {
	url           string
	withOutLabels []string

	addPrefix string
	addLabels map[string]string
}

func (ra *RemoteAggregator) Describe(ch chan<- *prometheus.Desc) {
	// No static descriptions, metrics are dynamic.
}

func (ra *RemoteAggregator) Collect(ch chan<- prometheus.Metric) {
	defer updateRunTime(ra.url, time.Now())

	resp, err := http.Get(ra.url)
	if err != nil {
		log.Error("error fetching metrics", "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Error("unexpected status code", "code", resp.StatusCode)
		return
	}

	ra.decodeAndSend(resp.Body, ch)
}

func (ra *RemoteAggregator) decodeAndSend(reader io.Reader, ch chan<- prometheus.Metric) {
	decoder := expfmt.NewDecoder(reader, expfmt.NewFormat(expfmt.TypeTextPlain))
	var metricFamily dto.MetricFamily

	for {
		err := decoder.Decode(&metricFamily)
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Error("error decoding metric family", "err", err)
			break
		}

		ra.aggregateAndSend(&metricFamily, ch)
	}
}

func (ra *RemoteAggregator) aggregateAndSend(metricFamily *dto.MetricFamily, ch chan<- prometheus.Metric) {

	aggregatedLabels, aggregatedValue := aggregateMetrics(metricFamily.Metric, ra.withOutLabels)

	for key, value := range aggregatedValue {

		var promMetric prometheus.Metric
		var err error

		// modify name and labels if required
		name := metricFamily.GetName()
		if ra.addPrefix != "" {
			name = ra.addPrefix + name
		}

		maps.Copy(aggregatedLabels[key], ra.addLabels)

		desc := prometheus.NewDesc(name, metricFamily.GetHelp(), nil, aggregatedLabels[key])

		switch metricFamily.GetType() {
		case dto.MetricType_GAUGE:
			promMetric, err = prometheus.NewConstMetric(desc, prometheus.GaugeValue, value)
		case dto.MetricType_COUNTER:
			promMetric, err = prometheus.NewConstMetric(desc, prometheus.CounterValue, value)
		default:
			promMetric, err = prometheus.NewConstMetric(desc, prometheus.UntypedValue, value)
		}

		if err != nil {
			log.Error("error creating Prometheus metric", "err", err)
			continue
		}

		ch <- promMetric
	}
}

// aggregateMetrics returns aggregated values and label pairs map on same key
func aggregateMetrics(metrics []*dto.Metric, withOutLabels []string) (map[string]map[string]string, map[string]float64) {
	ignoredSet := make(map[string]struct{}, len(withOutLabels))
	for _, label := range withOutLabels {
		ignoredSet[label] = struct{}{}
	}

	aggregatedValue := make(map[string]float64)
	aggregatedLabels := make(map[string]map[string]string)

	for _, metric := range metrics {
		filteredLabels := make(map[string]string)
		key := ""
		for _, label := range metric.Label {
			if _, found := ignoredSet[label.GetName()]; !found {
				filteredLabels[label.GetName()] = label.GetValue()
				key += label.GetName() + "=" + label.GetValue() + ","
			}
		}
		aggregatedLabels[key] = filteredLabels

		if metric.GetGauge() != nil {
			aggregatedValue[key] += metric.GetGauge().GetValue()
		} else if metric.GetCounter() != nil {
			aggregatedValue[key] += metric.GetCounter().GetValue()
		}
	}
	return aggregatedLabels, aggregatedValue
}

func updateRunTime(remoteURL string, start time.Time) {
	pcDuration.WithLabelValues(remoteURL).Observe(time.Since(start).Seconds())
}

func usage() {
	fmt.Fprintf(os.Stderr, "NAME:\n")
	fmt.Fprintf(os.Stderr, "\tmetrics-aggregator\n")

	fmt.Fprintf(os.Stderr, "DESCRIPTION:\n")
	fmt.Fprintf(os.Stderr, "\tA metrics aggregator to aggregate metrics without given labels.\n")

	fmt.Fprintf(os.Stderr, "OPTIONS:\n")
	fmt.Fprintf(os.Stderr, "\t--listen-address           (default: :9000)\n")
	fmt.Fprintf(os.Stderr, "\t--metrics-path             (default: /metrics)\n")
	fmt.Fprintf(os.Stderr, "\t--target-url               (default: 'http://localhost:8080/metrics')\n")
	fmt.Fprintf(os.Stderr, "\t--aggregate-without-label  (default: '')\n")
	fmt.Fprintf(os.Stderr, "\t--add-prefix               (default: '')\n")
	fmt.Fprintf(os.Stderr, "\t--add-labels               (default: '')\n")
	os.Exit(2)
}

func main() {
	port := flag.String("listen-address", ":9000", "address the metrics server binds to")
	metricPath := flag.String("metrics-path", "/metrics", "path under which to expose metrics")
	targetURL := flag.String("target-url", "http://localhost:8090/metrics", "remote target url to scrap metrics")
	withOutLabels := flag.String("aggregate-without-labels", "", "comma separated names of the labels which are removed from the aggregated metrics")
	addPrefix := flag.String("add-prefix", "", "given prefix will be added to all metrics name")
	addLabels := flag.String("add-labels", "", "comma separated list of key=value pairs which will be added to all metrics")

	flag.Usage = usage
	flag.Parse()

	log = slog.New(slog.NewTextHandler(
		os.Stderr,
		&slog.HandlerOptions{
			Level: slog.LevelInfo,
		},
	))

	log = slog.Default()

	if withOutLabels == nil || *withOutLabels == "" {
		log.Error("'aggregate-without-labels' is required!")
		os.Exit(1)
	}

	collector := &RemoteAggregator{
		url:           *targetURL,
		withOutLabels: strings.Split(*withOutLabels, ","),
		addPrefix:     *addPrefix,
		addLabels:     make(map[string]string),
	}

	for _, pair := range strings.Split(*addLabels, ",") {
		if pair == "" {
			continue
		}
		kv := strings.Split(pair, "=")
		if len(kv) == 2 {
			collector.addLabels[kv[0]] = kv[1]
		}
	}

	reg := prometheus.NewPedanticRegistry()

	reg.MustRegister(collector, pcDuration)

	log.Info("starting server", "port", *port, "metrics", *metricPath)

	http.Handle(*metricPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	if err := http.ListenAndServe(*port, nil); err != nil {
		log.Error("error starting HTTP server", "err", err)
		os.Exit(1)
	}
}
