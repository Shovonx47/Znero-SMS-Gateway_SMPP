package analytics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Bucket boundaries in milliseconds
var latencyBucketBounds = []int{50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000}

// bucketKey returns the histogram key string for a given latency in ms.
func bucketKey(latencyMs int) string {
	if latencyMs < 0 {
		latencyMs = 0
	}
	for _, bound := range latencyBucketBounds {
		if latencyMs <= bound {
			return fmt.Sprintf("le_%d", bound)
		}
	}
	return "gt_30000"
}

// NewLatencyHistogram returns an initialized histogram.
func NewLatencyHistogram() LatencyHistogram {
	h := make(LatencyHistogram)
	for _, b := range latencyBucketBounds {
		h[fmt.Sprintf("le_%d", b)] = 0
	}
	h["gt_30000"] = 0
	return h
}

// Observe adds a latency measurement to the histogram.
func (h LatencyHistogram) Observe(latencyMs int) {
	key := bucketKey(latencyMs)
	h[key]++
}

// Merge combines another histogram into this one.
func (h LatencyHistogram) Merge(other LatencyHistogram) {
	for k, v := range other {
		h[k] += v
	}
}

// TotalSamples returns the total count of observations across all buckets.
func (h LatencyHistogram) TotalSamples() int64 {
	var total int64
	for _, v := range h {
		total += v
	}
	return total
}

type bucketPair struct {
	upperBound int
	count      int64
}

// Percentile estimates the value at given percentile (e.g. 50, 95, 99) using linear interpolation.
func (h LatencyHistogram) Percentile(p float64) int {
	total := h.TotalSamples()
	if total == 0 {
		return 0
	}

	var pairs []bucketPair
	for _, b := range latencyBucketBounds {
		pairs = append(pairs, bucketPair{upperBound: b, count: h[fmt.Sprintf("le_%d", b)]})
	}
	pairs = append(pairs, bucketPair{upperBound: 60000, count: h["gt_30000"]})

	targetRank := (p / 100.0) * float64(total)
	var cumulative int64
	var lowerBound int

	for _, pair := range pairs {
		prevCumulative := cumulative
		cumulative += pair.count
		if float64(cumulative) >= targetRank {
			// Interpolate within this bucket
			bucketCount := pair.count
			if bucketCount <= 0 {
				return pair.upperBound
			}
			fraction := (targetRank - float64(prevCumulative)) / float64(bucketCount)
			if fraction < 0 {
				fraction = 0
			}
			interpolated := float64(lowerBound) + fraction*float64(pair.upperBound-lowerBound)
			return int(interpolated)
		}
		lowerBound = pair.upperBound
	}

	return 60000
}

// CalculatePercentiles returns (p50, p95, p99).
func (h LatencyHistogram) CalculatePercentiles() (int, int, int) {
	return h.Percentile(50), h.Percentile(95), h.Percentile(99)
}

// ParseBucketKey extracts the upper bound of a bucket key for sorting or display.
func ParseBucketKey(key string) int {
	if strings.HasPrefix(key, "le_") {
		v, _ := strconv.Atoi(strings.TrimPrefix(key, "le_"))
		return v
	}
	if key == "gt_30000" {
		return 60000
	}
	return 0
}

// SortedBucketKeys returns all bucket keys sorted by threshold.
func SortedBucketKeys() []string {
	keys := make([]string, 0, len(latencyBucketBounds)+1)
	for _, b := range latencyBucketBounds {
		keys = append(keys, fmt.Sprintf("le_%d", b))
	}
	keys = append(keys, "gt_30000")
	sort.Slice(keys, func(i, j int) bool {
		return ParseBucketKey(keys[i]) < ParseBucketKey(keys[j])
	})
	return keys
}
