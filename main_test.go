package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"google.golang.org/protobuf/proto"
)

func pointer(v string) *string { return &v }

func TestAggregateMetricss(t *testing.T) {
	metrics := []*dto.Metric{
		{
			Label: []*dto.LabelPair{
				{Name: pointer("l1"), Value: pointer("v1")},
			},
			Counter: &dto.Counter{Value: proto.Float64(10)},
		},
		{
			Label: []*dto.LabelPair{
				{Name: pointer("l1"), Value: pointer("v1")},
				{Name: pointer("l2"), Value: pointer("v2")},
			},
			Counter: &dto.Counter{Value: proto.Float64(20)},
		},
		{
			Label: []*dto.LabelPair{
				{Name: pointer("l1"), Value: pointer("v1")},
				{Name: pointer("l2"), Value: pointer("v2")},
				{Name: pointer("l3"), Value: pointer("v3")},
			},
			Counter: &dto.Counter{Value: proto.Float64(30)},
		},
	}

	tests := []struct {
		name                   string
		aggregateWithOutLabels []string
		wantAggregatedLabels   map[string]map[string]string
		wantAggregatedValues   map[string]float64
	}{
		{
			"no-matching-labels",
			[]string{"l4"},
			map[string]map[string]string{
				"l1=v1,":             {"l1": "v1"},
				"l1=v1,l2=v2,":       {"l1": "v1", "l2": "v2"},
				"l1=v1,l2=v2,l3=v3,": {"l1": "v1", "l2": "v2", "l3": "v3"},
			},
			map[string]float64{
				"l1=v1,":             10,
				"l1=v1,l2=v2,":       20,
				"l1=v1,l2=v2,l3=v3,": 30,
			},
		},
		{
			"matching-one",
			[]string{"l3"},
			map[string]map[string]string{
				"l1=v1,":       {"l1": "v1"},
				"l1=v1,l2=v2,": {"l1": "v1", "l2": "v2"},
			},
			map[string]float64{
				"l1=v1,":       10,
				"l1=v1,l2=v2,": 50,
			},
		},
		{
			"matching-two",
			[]string{"l2"},
			map[string]map[string]string{
				"l1=v1,":       {"l1": "v1"},
				"l1=v1,l3=v3,": {"l1": "v1", "l3": "v3"},
			},
			map[string]float64{
				"l1=v1,":       30,
				"l1=v1,l3=v3,": 30,
			},
		},
		{
			"matching-all",
			[]string{"l1"},
			map[string]map[string]string{
				"":             {},
				"l2=v2,":       {"l2": "v2"},
				"l2=v2,l3=v3,": {"l2": "v2", "l3": "v3"},
			},
			map[string]float64{
				"":             10,
				"l2=v2,":       20,
				"l2=v2,l3=v3,": 30,
			},
		},
		{
			"multiple-labels",
			[]string{"l2", "l3"},
			map[string]map[string]string{
				"l1=v1,": {"l1": "v1"},
			},
			map[string]float64{
				"l1=v1,": 60,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aggregatedLabels, aggregatedValues := aggregateMetrics(metrics, tt.aggregateWithOutLabels)

			if diff := cmp.Diff(aggregatedLabels, tt.wantAggregatedLabels, cmpopts.IgnoreUnexported(dto.LabelPair{})); diff != "" {
				t.Errorf("filteredLabels mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(aggregatedValues, tt.wantAggregatedValues); diff != "" {
				t.Errorf("aggregatedValues mismatch (-want +got):\n%s", diff)
			}

		})
	}
}

func Test_Collector(t *testing.T) {
	log = slog.Default()

	originalMetrics := `
# HELP component_received_events_total component_received_events_total
# TYPE component_received_events_total counter
component_received_events_total{l1="v1"} 10 1735054883000
component_received_events_total{l1="v1",l2="v2"} 20 1735054879000
component_received_events_total{l1="v1",l2="v2",l3="v3"} 30 1735054866000
# HELP component_received_event_bytes_total component_received_event_bytes_total
# TYPE component_received_event_bytes_total counter
component_received_event_bytes_total{l1="v1"} 1000 1735054883000
component_received_event_bytes_total{l1="v1",l2="v2"} 2000 1735054879000
component_received_event_bytes_total{l1="v1",l2="v2",l3="v3"} 3000 1735054866000
`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, originalMetrics)
	}))
	defer ts.Close()

	tests := []struct {
		name                   string
		aggregateWithOutLabels []string
		want                   string
	}{
		{
			"no-matching-labels",
			[]string{"l4"},
			`# HELP component_received_event_bytes_total component_received_event_bytes_total
# TYPE component_received_event_bytes_total counter
component_received_event_bytes_total{l1="v1"} 1000
component_received_event_bytes_total{l1="v1",l2="v2"} 2000
component_received_event_bytes_total{l1="v1",l2="v2",l3="v3"} 3000
# HELP component_received_events_total component_received_events_total
# TYPE component_received_events_total counter
component_received_events_total{l1="v1"} 10
component_received_events_total{l1="v1",l2="v2"} 20
component_received_events_total{l1="v1",l2="v2",l3="v3"} 30
`,
		},
		{
			"matching-one",
			[]string{"l3"},
			`# HELP component_received_event_bytes_total component_received_event_bytes_total
# TYPE component_received_event_bytes_total counter
component_received_event_bytes_total{l1="v1"} 1000
component_received_event_bytes_total{l1="v1",l2="v2"} 5000
# HELP component_received_events_total component_received_events_total
# TYPE component_received_events_total counter
component_received_events_total{l1="v1"} 10
component_received_events_total{l1="v1",l2="v2"} 50
`,
		},
		{
			"matching-two",
			[]string{"l2"},
			`# HELP component_received_event_bytes_total component_received_event_bytes_total
# TYPE component_received_event_bytes_total counter
component_received_event_bytes_total{l1="v1"} 3000
component_received_event_bytes_total{l1="v1",l3="v3"} 3000
# HELP component_received_events_total component_received_events_total
# TYPE component_received_events_total counter
component_received_events_total{l1="v1"} 30
component_received_events_total{l1="v1",l3="v3"} 30
`,
		},
		{
			"matching-all",
			[]string{"l1"},
			`# HELP component_received_event_bytes_total component_received_event_bytes_total
# TYPE component_received_event_bytes_total counter
component_received_event_bytes_total 1000
component_received_event_bytes_total{l2="v2"} 2000
component_received_event_bytes_total{l2="v2",l3="v3"} 3000
# HELP component_received_events_total component_received_events_total
# TYPE component_received_events_total counter
component_received_events_total 10
component_received_events_total{l2="v2"} 20
component_received_events_total{l2="v2",l3="v3"} 30
`,
		},
		{
			"multiple-labels",
			[]string{"l2", "l3"},
			`# HELP component_received_event_bytes_total component_received_event_bytes_total
# TYPE component_received_event_bytes_total counter
component_received_event_bytes_total{l1="v1"} 6000
# HELP component_received_events_total component_received_events_total
# TYPE component_received_events_total counter
component_received_events_total{l1="v1"} 60
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &RemoteAggregator{
				url:                    ts.URL,
				aggregateWithOutLabels: tt.aggregateWithOutLabels,
			}

			reg := prometheus.NewPedanticRegistry()
			reg.MustRegister(collector)

			gathering, err := reg.Gather()
			if err != nil {
				t.Errorf("JSONCollector.process() error = %v", err)
			}

			got := metricsToText(gathering)

			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("collector output mismatch (-want +got):\n%s", diff)
			}
		})
	}

}

func metricsToText(gathering []*dto.MetricFamily) string {
	out := &bytes.Buffer{}
	for _, mf := range gathering {
		if _, err := expfmt.MetricFamilyToText(out, mf); err != nil {
			panic(err)
		}
	}
	return out.String()
}
