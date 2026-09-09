package tools

import "time"

// APIVersion identifies the response contract (spec §3.2).
const APIVersion = "mcp-k8s/v1"

// Cluster identifies which cluster produced the data. Every tool call
// carries an explicit cluster argument (spec §4), so every response can
// state which cluster it came from.
type Cluster struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Coverage declares the window a time-bound response provably covers
// (spec §3.3). complete=false forces the consumer to treat absence as
// unknown, not as "no failures".
type Coverage struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Complete bool      `json:"complete"`
	Reason   string    `json:"reason,omitempty"`
}

// Meta accompanies every response (spec §3.2).
type Meta struct {
	Timestamp  time.Time `json:"timestamp"`
	DurationMs int64     `json:"durationMs"`
	View       string    `json:"view,omitempty"`
	// Truncated marks responses whose beginning was cut (logs, spec §3.5).
	Truncated    bool `json:"truncated,omitempty"`
	DroppedLines int  `json:"droppedLines,omitempty"`
}

// Envelope is the response contract for every tool (spec §3.2). A tool
// returns either Data or Error, never both.
type Envelope struct {
	APIVersion string    `json:"apiVersion"`
	Cluster    Cluster   `json:"cluster"`
	Data       any       `json:"data,omitempty"`
	Coverage   *Coverage `json:"coverage,omitempty"`
	Error      *Error    `json:"error,omitempty"`
	Meta       Meta      `json:"meta"`
}

// NewEnvelope builds a success envelope for the given cluster and data.
func NewEnvelope(cluster Cluster, data any, meta Meta) *Envelope {
	return &Envelope{
		APIVersion: APIVersion,
		Cluster:    cluster,
		Data:       data,
		Meta:       meta,
	}
}

// NewErrorEnvelope builds an error envelope (spec §3.6).
func NewErrorEnvelope(cluster Cluster, err *Error, meta Meta) *Envelope {
	return &Envelope{
		APIVersion: APIVersion,
		Cluster:    cluster,
		Error:      err,
		Meta:       meta,
	}
}
