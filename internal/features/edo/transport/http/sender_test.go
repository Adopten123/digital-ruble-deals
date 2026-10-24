package edohttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Adopten123/digital-ruble-deals/internal/features/edo/service"
)

func TestSenderRejectsRedirectAndUntrustedTargets(t *testing.T) {
	for _, target := range []string{"https://127.0.0.1/internal/edo/events", "http://example.com/internal/edo/events", "http://127.0.0.1/other", "http://user:pass@127.0.0.1/internal/edo/events", "http://127.0.0.1/internal/edo/events?secret=1"} {
		if _, err := NewSender(target); err == nil {
			t.Fatal("untrusted sender target", target)
		}
	}
	called := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.Write([]byte(`{}`)) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	sender, err := NewSender(source.URL + "/internal/edo/events")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sender.Send(context.Background(), service.Headers{}, []byte(`{}`)); err == nil || called {
		t.Fatal("sender followed a redirect or accepted it", err, called)
	}
}
