package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// AvatarsUploadsTotal counts avatar upload attempts by outcome.
	AvatarsUploadsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "avatars_uploads_total",
			Help: "Total number of avatar uploads.",
		},
		[]string{"status"},
	)

	// AvatarsUploadDuration observes avatar upload latency by outcome.
	AvatarsUploadDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "avatars_upload_duration_seconds",
			Help:    "Avatar upload duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status"},
	)

	// AvatarsStorageBytes tracks the aggregate storage used by all stored
	// original avatar files, in bytes. Unlabeled to avoid a per-user
	// cardinality blowup in Prometheus.
	AvatarsStorageBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "avatars_storage_bytes",
			Help: "Total storage used by avatars, in bytes.",
		},
	)

	// AvatarsDeletionsTotal counts avatar deletion attempts by outcome.
	AvatarsDeletionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "avatars_deletions_total",
			Help: "Total number of avatar deletions.",
		},
		[]string{"status"},
	)

	// AvatarsResizeDuration observes thumbnail generation latency by outcome.
	AvatarsResizeDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "avatars_resize_duration_seconds",
			Help:    "Avatar thumbnail generation duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status"},
	)

	// AvatarsJobRetriesTotal counts worker job retries by queue.
	AvatarsJobRetriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "avatars_job_retries_total",
			Help: "Total number of retried worker jobs.",
		},
		[]string{"queue"},
	)
)
