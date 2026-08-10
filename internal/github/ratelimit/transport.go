package ratelimit

import (
	"net/http"
)

// Transport applies a [Limiter] to every request that passes through it, and
// records what every response says about the budget.
//
// It is a RoundTripper rather than a wrapper at each call site because the HTTP
// method is the only reliable answer to "is this a write". A call site can
// label itself wrong; a POST cannot. It also catches the requests nothing in
// this tree issues explicitly — the ones go-github makes on its own for
// pagination.
//
// It sits **under** the 5xx retry wrapper, closest to the network, so that a
// retried attempt is paced and observed like any other request rather than
// slipping past the bucket because the first attempt already paid for it.
type Transport struct {
	Next    http.RoundTripper
	Limiter *Limiter
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Limiter != nil {
		if err := t.Limiter.Await(req.Context(), isWrite(req.Method)); err != nil {
			return nil, err
		}
	}

	resp, err := t.next().RoundTrip(req)

	// Observed even when the request failed: a 403 that is a rate limit carries
	// the same headers, and those are the readings most worth having.
	if resp != nil && t.Limiter != nil {
		t.Limiter.Observe(resp.Header)
	}

	return resp, err
}

// next is the underlying transport, defaulting to the standard one.
func (t *Transport) next() http.RoundTripper {
	if t.Next != nil {
		return t.Next
	}

	return http.DefaultTransport
}

// isWrite reports whether a method creates or changes content, which is what
// GitHub's secondary limit counts.
func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}
