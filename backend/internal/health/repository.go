package health

import "context"

// Probe checks one dependency. Ping must honour ctx cancellation.
type Probe interface {
	Name() string
	Ping(ctx context.Context) error
}

type funcProbe struct {
	name string
	ping func(ctx context.Context) error
}

func (p funcProbe) Name() string                   { return p.name }
func (p funcProbe) Ping(ctx context.Context) error { return p.ping(ctx) }

// Pinger is satisfied by *pgxpool.Pool and *kafkaclient.Client.
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewProbe adapts a Pinger into a named Probe. This is the health module's data-access layer:
// it is the only place that talks to the infrastructure being checked.
func NewProbe(name string, p Pinger) Probe {
	return funcProbe{name: name, ping: p.Ping}
}
