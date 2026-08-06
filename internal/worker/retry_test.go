package worker

import (
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestRetryAttempt(t *testing.T) {
	tests := []struct {
		name    string
		headers amqp.Table
		want    int
	}{
		{name: "nil headers", headers: nil, want: 0},
		{name: "no header set", headers: amqp.Table{}, want: 0},
		{
			name:    "int32 header",
			headers: amqp.Table{retryCountHeader: int32(3)},
			want:    3,
		},
		{
			name:    "int64 header",
			headers: amqp.Table{retryCountHeader: int64(2)},
			want:    2,
		},
		{
			name:    "int header",
			headers: amqp.Table{retryCountHeader: 5},
			want:    5,
		},
		{
			name:    "unexpected type",
			headers: amqp.Table{retryCountHeader: "3"},
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryAttempt(tt.headers); got != tt.want {
				t.Fatalf("retryAttempt() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBackoffDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: baseRetryDelay},
		{attempt: 2, want: baseRetryDelay * 2},
		{attempt: 3, want: baseRetryDelay * 4},
		{attempt: 4, want: baseRetryDelay * 8},
		{attempt: 10, want: maxRetryDelay},
	}

	for _, tt := range tests {
		if got := backoffDelay(tt.attempt); got != tt.want {
			t.Fatalf(
				"backoffDelay(%d) = %s, want %s",
				tt.attempt, got, tt.want,
			)
		}
	}
}
