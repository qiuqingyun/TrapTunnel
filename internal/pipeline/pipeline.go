package pipeline

import (
	"context"
	"time"

	"traptunnel/internal/frame"
)

// Envelope carries a frame plus lightweight runtime metadata between pipeline stages.
type Envelope struct {
	Frame      frame.Frame
	Source     string
	ReceivedAt time.Time
}

// Sink is the common contract for future inject/egress/export outputs.
type Sink interface {
	Name() string
	Publish(context.Context, Envelope) error
}

// SinkFunc adapts a function to the Sink interface.
type SinkFunc struct {
	SinkName string
	Handler  func(context.Context, Envelope) error
}

func (s SinkFunc) Name() string {
	return s.SinkName
}

func (s SinkFunc) Publish(ctx context.Context, env Envelope) error {
	return s.Handler(ctx, env)
}

// Broadcaster fans one envelope out to a set of sinks.
type Broadcaster struct {
	sinks []Sink
}

func NewBroadcaster(sinks ...Sink) Broadcaster {
	return Broadcaster{sinks: sinks}
}

func (b Broadcaster) Publish(ctx context.Context, env Envelope) []error {
	errs := make([]error, 0)
	for _, sink := range b.sinks {
		if err := sink.Publish(ctx, env); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
