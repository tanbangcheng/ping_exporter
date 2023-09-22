package tcpping

import (
	"github.com/prometheus/client_golang/prometheus"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

type pingResult struct {
	remote string
	rtt    time.Duration
}

type Collector struct {
	LatencyMaxUs *prometheus.GaugeVec
	Send         *prometheus.GaugeVec
	Lost         *prometheus.GaugeVec

	ring chan *pingResult

	remotes []string

	mu sync.Mutex
}

func NewCollector(remotes []string) *Collector {
	c := &Collector{
		LatencyMaxUs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace:   "tcp_ping",
			Name:        "latency_max_us",
			Help:        "",
			ConstLabels: nil,
		}, []string{"remote"}),
		Send: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace:   "tcp_ping",
			Name:        "send",
			Help:        "",
			ConstLabels: nil,
		}, []string{"remote"}),
		Lost: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace:   "tcp_ping",
			Name:        "lost",
			Help:        "",
			ConstLabels: nil,
		}, []string{"remote"}),
		ring:    make(chan *pingResult, 10000),
		remotes: remotes,
	}

	return c
}

func (c *Collector) Describe(descs chan<- *prometheus.Desc) {
	c.LatencyMaxUs.Describe(descs)
	c.Lost.Describe(descs)
	c.Send.Describe(descs)
}

func (c *Collector) Collect(metrics chan<- prometheus.Metric) {
	c.mu.Lock()
	results := make([]*pingResult, 0, len(c.ring))

	func() {
		for {
			select {
			case r := <-c.ring:
				results = append(results, r)
			default:
				return
			}
		}
	}()
	c.mu.Unlock()

	c.LatencyMaxUs.Reset()
	c.Lost.Reset()
	c.Send.Reset()

	max := map[string]int64{}

	for _, r := range results {
		c.Send.WithLabelValues(r.remote).Add(1)
		if r.rtt == 0 {
			c.Lost.WithLabelValues(r.remote).Add(1)
		}
		m := max[r.remote]
		if m < r.rtt.Microseconds() {
			max[r.remote] = r.rtt.Microseconds()
		}
	}

	for k, v := range max {
		c.LatencyMaxUs.WithLabelValues(k).Set(float64(v))
	}

	c.LatencyMaxUs.Collect(metrics)
	c.Lost.Collect(metrics)
	c.Send.Collect(metrics)
}

func (c *Collector) ping(remote string) time.Duration {
	start := time.Now()
	cc, err := net.DialTimeout("tcp4", remote, 100*time.Millisecond)
	if err != nil {
		if !strings.Contains(err.Error(), "refused") {
			log.Printf("error dialing %s: %v", remote, err)
		}
		if strings.Contains(err.Error(), "timeout") {
			return 0
		}
	}
	cost := time.Since(start)
	if cc != nil {
		cc.Close()
	}
	return cost
}

func (c *Collector) Run() {
	for {
		results := make([]*pingResult, len(c.remotes))

		wg := sync.WaitGroup{}
		for i := range c.remotes {
			idx := i
			r := c.remotes[i]
			wg.Add(1)
			go func() {
				defer wg.Done()
				rtt := c.ping(r)
				results[idx] = &pingResult{
					remote: r,
					rtt:    rtt,
				}
			}()
		}
		wg.Wait()

		c.mu.Lock()
		for _, r := range results {
			select {
			case c.ring <- r:
			default:
			}
		}
		c.mu.Unlock()

		time.Sleep(100 * time.Millisecond)
	}
}
