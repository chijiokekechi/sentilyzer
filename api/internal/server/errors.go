package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/chijiokekechi/sentilyzer/api/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errYAMLRequestBody rejects YAML input. See decodeRequest for why.
var errYAMLRequestBody = errors.New(
	"YAML request bodies are not supported — send application/json or " +
		"application/xml. To receive YAML, use ?format=yaml or " +
		"Accept: application/yaml",
)

// classify sorts an error into the one category both transports agree on, so
// REST and gRPC cannot drift apart on what a failure means.
//
// Before this, every AnalyzeTopic failure became 502 over REST and
// InvalidArgument over gRPC — so an empty topic (the caller's fault), a dead
// upstream, a timeout, and a server with no credentials configured were all
// reported identically, and neither the caller nor the operator could tell
// which had happened.
type category int

const (
	catInternal    category = iota // we broke; the caller can't help
	catBadRequest                  // the caller sent something invalid
	catUnsupported                 // the caller used a media type we don't accept
	catUpstream                    // an upstream we depend on failed
	catTimeout                     // something took too long
	catNoCapacity                  // we are not configured to serve this at all
)

func classify(err error) category {
	switch {
	case err == nil:
		return catInternal
	case errors.Is(err, service.ErrTopicRequired), errors.Is(err, service.ErrInvalidRequest):
		return catBadRequest
	case errors.Is(err, errYAMLRequestBody):
		return catUnsupported
	case errors.Is(err, service.ErrNoPlatforms):
		return catNoCapacity
	case errors.Is(err, service.ErrAllConnectorsFailed):
		return catUpstream
	case errors.Is(err, context.DeadlineExceeded):
		return catTimeout
	}
	// A gRPC status may be wrapped several layers down by the inference
	// client. status.Code unwraps, and returns Unknown for non-status errors,
	// so this must run after the sentinel checks above.
	switch status.Code(err) {
	case codes.DeadlineExceeded:
		return catTimeout
	case codes.Unavailable:
		return catUpstream
	case codes.ResourceExhausted:
		// The worker rejected a batch the client should have split. That is
		// our bug, not the caller's.
		return catInternal
	case codes.InvalidArgument:
		return catBadRequest
	}
	return catInternal
}

func httpStatusFor(err error) int {
	switch classify(err) {
	case catBadRequest:
		return http.StatusBadRequest // 400
	case catUnsupported:
		return http.StatusUnsupportedMediaType // 415
	case catUpstream:
		return http.StatusBadGateway // 502
	case catTimeout:
		return http.StatusGatewayTimeout // 504
	case catNoCapacity:
		return http.StatusServiceUnavailable // 503
	default:
		return http.StatusInternalServerError // 500
	}
}

func grpcCodeFor(err error) codes.Code {
	switch classify(err) {
	case catBadRequest:
		return codes.InvalidArgument
	case catUnsupported:
		return codes.InvalidArgument
	case catUpstream:
		return codes.Unavailable
	case catTimeout:
		return codes.DeadlineExceeded
	case catNoCapacity:
		return codes.FailedPrecondition
	default:
		return codes.Internal
	}
}
