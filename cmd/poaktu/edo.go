package main

import (
	"errors"
	"net"
	"net/url"
	"strconv"
)

// The EDO sender reaches this process directly; the browser origin and reverse
// proxy are deliberately excluded. A configured remote destination is never used.
func internalEDOURL(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", errors.New("HTTP_ADDR must include a fixed port for internal EDO delivery")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("HTTP_ADDR must include a valid fixed port")
	}
	switch host {
	case "", "0.0.0.0", "localhost":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	default:
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", errors.New("HTTP_ADDR must bind loopback or a wildcard for internal EDO delivery")
		}
		host = ip.String()
	}
	target := url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(n)), Path: "/internal/edo/events"}
	return target.String(), nil
}
