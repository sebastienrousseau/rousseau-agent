package observability

import "github.com/prometheus/client_golang/prometheus"

// TurnStops counts how turns ended, by stop reason. "max_tokens" is
// the one to alert on: the model's reply was cut off at its output
// limit and the sender received a truncated answer with a marker.
var TurnStops = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "rousseau_turn_stops_total",
	Help: "Turns ended, by provider stop reason (end_turn, max_tokens, other).",
}, []string{"reason"})
