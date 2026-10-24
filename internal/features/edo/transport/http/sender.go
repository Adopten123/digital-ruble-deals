package edohttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/features/edo/service"
)

type HTTPSender struct {
	target string
	client *http.Client
}

// Target is supplied by trusted process configuration, never by an operator's
// body. Proxy environment and redirects cannot send the source secret elsewhere.
func NewSender(target string) (*HTTPSender, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/internal/edo/events" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, errors.New("EDO delivery target must be a loopback internal HTTP endpoint")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, ResponseHeaderTimeout: 4 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 2}
	return &HTTPSender{target: target, client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (s *HTTPSender) Send(ctx context.Context, h service.Headers, body []byte) (service.Response, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, s.target, bytes.NewReader(body))
	if err != nil {
		return service.Response{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-EDO-Key-ID", h.KeyID)
	r.Header.Set("X-EDO-Timestamp", h.Timestamp)
	r.Header.Set("X-EDO-Signature", h.Signature)
	response, err := s.client.Do(r)
	if err != nil {
		return service.Response{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, service.MaxBody+1))
	if err != nil {
		return service.Response{}, err
	}
	var object map[string]json.RawMessage
	if len(raw) > service.MaxBody || json.Unmarshal(raw, &object) != nil || object == nil || response.StatusCode >= 300 && response.StatusCode < 400 {
		return service.Response{}, errors.New("invalid EDO delivery response")
	}
	return service.Response{Status: response.StatusCode, Body: raw}, nil
}
