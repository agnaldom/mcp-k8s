package services

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agnaldom/mcp-k8s/internal/policy"
)

// CoverageInfo declares the window the response provably covers
// (spec §3.3). Complete=false forces the consumer to treat absence as
// unknown, not as "no failures".
type CoverageInfo struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Complete bool      `json:"complete"`
	Reason   string    `json:"reason,omitempty"`
}

// NormalizedEvent is the normalized event shape for k8s_events_list.
type NormalizedEvent struct {
	Type           string    `json:"type"`
	Reason         string    `json:"reason"`
	Message        string    `json:"message"`
	InvolvedKind   string    `json:"involvedKind"`
	InvolvedName   string    `json:"involvedName"`
	InvolvedNS     string    `json:"involvedNamespace,omitempty"`
	Count          int32     `json:"count"`
	FirstTimestamp time.Time `json:"firstTimestamp"`
	LastTimestamp  time.Time `json:"lastTimestamp"`
	Source         string    `json:"source,omitempty"`
}

// EventsOptions carries the validated k8s_events_list arguments.
type EventsOptions struct {
	Cluster   string
	Namespace string // empty = all namespaces
	// Since narrows the window; nil means everything available.
	Since *time.Duration
	// Limit caps the newest events returned, below the service maximum.
	// Zero means the service maximum.
	Limit int
}

// EventsResult is the data block plus coverage for k8s_events_list.
type EventsResult struct {
	Items    []NormalizedEvent `json:"items"`
	Coverage CoverageInfo      `json:"coverage"`
}

// EventService lists normalized events with declared coverage
// (spec §13 step 13).
type EventService struct {
	Policy   *policy.Policy
	Clients  TypedClients
	MaxItems int
	// now is injectable for tests.
	now func() time.Time
}

func NewEventService(pol *policy.Policy, clients TypedClients, maxItems int) *EventService {
	return &EventService{Policy: pol, Clients: clients, MaxItems: maxItems, now: time.Now}
}

// Events lists events in the requested window. Events are capped to the
// newest MaxItems; coverage always reflects what the response provably
// contains, never what the caller hoped to see.
func (s *EventService) Events(ctx context.Context, opts EventsOptions) (*EventsResult, error) {
	if err := s.Policy.ClusterAllowed(opts.Cluster); err != nil {
		return nil, err
	}
	clients, err := s.Clients(ctx, opts.Cluster)
	if err != nil {
		return nil, err
	}
	list, err := clients.CoreV1().Events(opts.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}

	windowStart := time.Time{}
	if opts.Since != nil {
		windowStart = s.now().Add(-*opts.Since)
	}
	var events []NormalizedEvent
	streamExtendsBeforeWindow := false
	for _, e := range list.Items {
		last := e.LastTimestamp.Time
		if last.IsZero() {
			last = e.CreationTimestamp.Time
		}
		if !windowStart.IsZero() {
			if last.Before(windowStart) {
				// Not in the window, but its existence proves the event
				// stream reaches before the window: coverage is complete.
				streamExtendsBeforeWindow = true
				continue
			}
		}
		src := e.Source.Component
		if src == "" && e.ReportingController != "" {
			src = e.ReportingController
		}
		first := e.FirstTimestamp.Time
		if first.IsZero() {
			first = e.CreationTimestamp.Time
		}
		events = append(events, NormalizedEvent{
			Type:           e.Type,
			Reason:         e.Reason,
			Message:        e.Message,
			InvolvedKind:   e.InvolvedObject.Kind,
			InvolvedName:   e.InvolvedObject.Name,
			InvolvedNS:     e.InvolvedObject.Namespace,
			Count:          e.Count,
			FirstTimestamp: first,
			LastTimestamp:  last,
			Source:         src,
		})
	}
	sort.Slice(events, func(a, b int) bool {
		return events[a].LastTimestamp.Before(events[b].LastTimestamp)
	})

	// Keep the newest MaxItems; the oldest kept defines coverage.from.
	maxItems := s.MaxItems
	if opts.Limit > 0 && opts.Limit < maxItems {
		maxItems = opts.Limit
	}
	if len(events) > maxItems {
		events = events[len(events)-maxItems:]
	}

	now := s.now()
	coverage := CoverageInfo{To: now}
	switch {
	case len(events) == 0 && streamExtendsBeforeWindow:
		// Stream reaches before the window but nothing occurred in it:
		// that IS the data — absence is real, not unknown.
		coverage.From = windowStart
		coverage.Complete = true
	case len(events) == 0:
		coverage.Complete = false
		coverage.Reason = "no events available in the window; absence of data is not absence of failures (spec §3.3)"
	case !windowStart.IsZero() && !streamExtendsBeforeWindow && events[0].LastTimestamp.After(windowStart):
		coverage.From = events[0].LastTimestamp
		coverage.Complete = false
		coverage.Reason = "oldest available event is newer than the requested window (likely kube-apiserver --event-ttl)"
	default:
		coverage.From = events[0].LastTimestamp
		coverage.Complete = true
	}
	return &EventsResult{Items: events, Coverage: coverage}, nil
}
