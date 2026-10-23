package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestCSRFBoundToCookieSessionAndOrigin(t *testing.T) {
	guard := NewCSRF(make([]byte, 32), "http://localhost:8080", false)
	request := httptest.NewRequest("GET", "http://localhost:8080/api/v1/auth/csrf", nil)
	response := httptest.NewRecorder()
	token, err := guard.Issue(response, request, "", true)
	if err != nil {
		t.Fatal(err)
	}
	cookie := response.Result().Cookies()[0]
	mutation := func(origin, session, header string) bool {
		r := httptest.NewRequest("POST", "http://localhost:8080/api/v1/auth/login", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", header)
		r.AddCookie(cookie)
		if session != "" {
			r.Header.Add("Cookie", SessionCookie+"="+session)
		}
		return guard.Check(httptest.NewRecorder(), r)
	}
	if !mutation("http://localhost:8080", "", token) {
		t.Fatal("valid preauth token rejected")
	}
	if mutation("https://attacker.example", "", token) || mutation("", "", token) || mutation("http://localhost:8080", "changed-session", token) || mutation("http://localhost:8080", "", "wrong") {
		t.Fatal("CSRF bypass")
	}
	if guard.valid(token+"x", "") {
		t.Fatal("tampered signature accepted")
	}
}
