package bridge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type hubTransportError struct{ err error }

func (e *hubTransportError) Error() string { return e.err.Error() }
func (e *hubTransportError) Unwrap() error { return e.err }

// Bind even SDK-detached SSE/DELETE requests to their owning generation.
// Retain terminal HTTP status without parsing SDK error strings.
type hubRoundTripper struct {
	base       http.RoundTripper
	ctx        context.Context
	generation *hubGeneration
}

type hubResponseBody struct {
	io.ReadCloser
	stop   func() bool
	cancel context.CancelFunc
}

func (b *hubResponseBody) Close() error {
	b.stop()
	b.cancel()
	return b.ReadCloser.Close()
}

func (rt *hubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(rt.ctx, cancel)
	if rt.ctx.Err() != nil {
		cancel()
	}
	response, err := rt.base.RoundTrip(req.Clone(ctx))
	if err != nil {
		if req.Context().Err() == nil && rt.ctx.Err() == nil {
			rt.generation.transportFailed.Store(true)
		}
		stop()
		cancel()
		var cert *tls.CertificateVerificationError
		var unknown x509.UnknownAuthorityError
		var invalid x509.CertificateInvalidError
		var hostname x509.HostnameError
		var record tls.RecordHeaderError
		if errors.As(err, &cert) || errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname) || errors.As(err, &record) {
			rt.generation.terminal.Store(&hubTransportError{errors.New("Hub TLS verification failed")})
		}
		return nil, err
	}
	status := response.StatusCode
	if status == 401 || status == 403 || (status >= 300 && status < 400) {
		rt.generation.terminal.Store(&hubTransportError{fmt.Errorf("Hub HTTP %d (%s); recovery refused", status, http.StatusText(status))})
	}
	if response.Body == nil {
		stop()
		cancel()
	} else {
		response.Body = &hubResponseBody{response.Body, stop, cancel}
	}
	return response, nil
}
