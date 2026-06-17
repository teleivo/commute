package server

import "github.com/prometheus/client_golang/prometheus"

// storeCollector is a prometheus.Collector that exposes per-node GCounter increments from the
// store's PNCounters. Metrics are computed at scrape time so no write-path overhead is added.
type storeCollector struct {
	store *Store
	desc  *prometheus.Desc
}

func newStoreCollector(store *Store) *storeCollector {
	return &storeCollector{
		store: store,
		desc: prometheus.NewDesc(
			"commute_gcounter_increments_total",
			"Per-node increment tally of the PNCounter's internal GCounter.",
			[]string{"key", "node"},
			nil,
		),
	}
}

func (c *storeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *storeCollector) Collect(ch chan<- prometheus.Metric) {
	for key, nodes := range c.store.CounterIncrements() {
		for node, v := range nodes {
			ch <- prometheus.MustNewConstMetric(
				c.desc,
				prometheus.GaugeValue,
				float64(v),
				key, string(node),
			)
		}
	}
}
