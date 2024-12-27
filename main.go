package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/urfave/cli/v3"
)

var (
	log = slog.New(slog.NewTextHandler(
		os.Stderr,
		&slog.HandlerOptions{
			Level: slog.LevelInfo,
		},
	))

	pcDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "metrics_aggregation_duration_seconds",
		Help: "Duration of a collection",
	},
		[]string{"remote"},
	)

	flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "metrics-bind-address",
			Value: ":9090",
			Usage: "The address the metric endpoint binds to.",
		},
		&cli.StringFlag{
			Name:  "metrics-path",
			Value: "/metrics",
			Usage: "The path under which to expose metrics.",
		},
		&cli.StringFlag{
			Name:     "target-url",
			Usage:    "The remote target metrics url to scrap metrics.",
			Required: true,
		},
		&cli.StringSliceFlag{
			Name:     "aggregate-without-label",
			Usage:    "The metrics will be aggregated over all label except listed labels. Labels will be removed from the result vector, while all other labels are preserved in the output.",
			Required: true,
		},
		&cli.StringSliceFlag{
			Name:  "include-metric",
			Usage: "The name of the scrapped metrics which will be aggregated and exported. if its not set all metrics will be exported from target.",
		},
		&cli.StringFlag{
			Name:  "add-prefix",
			Usage: "The prefix which will be added to all exported metrics name.",
		},
		&cli.StringSliceFlag{
			Name:  "add-labelValue",
			Usage: "The list of key=value pairs which will be added to all exported metrics.",
		},
	}
)

type RemoteAggregator struct {
	url                    string
	includeMetrics         []string
	aggregateWithOutLabels []string

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

		ra.processAndSend(&metricFamily, ch)
	}
}

func (ra *RemoteAggregator) processAndSend(metricFamily *dto.MetricFamily, ch chan<- prometheus.Metric) {

	name := metricFamily.GetName()
	// if includeMetrics is set filter metrics based on name
	if len(ra.includeMetrics) > 0 && !slices.Contains(ra.includeMetrics, name) {
		return
	}

	if ra.addPrefix != "" {
		name = ra.addPrefix + name
	}

	aggregatedLabels, aggregatedValue := aggregateMetrics(metricFamily.Metric, ra.aggregateWithOutLabels)

	for key, value := range aggregatedValue {
		var promMetric prometheus.Metric
		var err error

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
func aggregateMetrics(metrics []*dto.Metric, aggregateWithOutLabels []string) (map[string]map[string]string, map[string]float64) {
	aggregatedValue := make(map[string]float64)
	aggregatedLabels := make(map[string]map[string]string)

	for _, metric := range metrics {

		var key string
		filteredLabels := make(map[string]string)

		for _, label := range metric.Label {
			if !slices.Contains(aggregateWithOutLabels, label.GetName()) {
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

func main() {
	cmd := &cli.Command{
		Name:  "metrics-aggregator",
		Usage: "ggregate metrics to reduce cardinality by removing labels",
		Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {

			collector := &RemoteAggregator{
				url:                    cmd.String("target-url"),
				includeMetrics:         cmd.StringSlice("include-metric"),
				aggregateWithOutLabels: cmd.StringSlice("aggregate-without-label"),
				addPrefix:              cmd.String("add-prefix"),
				addLabels:              make(map[string]string),
			}

			for _, pair := range cmd.StringSlice("add-labelValue") {
				kv := strings.Split(pair, "=")
				if len(kv) == 2 {
					collector.addLabels[kv[0]] = kv[1]
				}
			}

			reg := prometheus.NewPedanticRegistry()

			reg.MustRegister(collector, pcDuration)

			log.Info("starting server", "port", cmd.String("metrics-bind-address"), "metrics", cmd.String("metrics-path"))

			http.Handle(cmd.String("metrics-path"), promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

			if err := http.ListenAndServe(cmd.String("metrics-bind-address"), nil); err != nil {
				return fmt.Errorf("error starting HTTP server %w", err)
			}

			return nil
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Error("error running app", "err", err)
		os.Exit(1)
	}

}
