package dataplane

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProxyFullDuplexInput(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).EnableFullDuplex()
		first := make([]byte, 1)
		if _, err := io.ReadFull(r.Body, first); err != nil {
			t.Error(err)
			return
		}
		_, _ = w.Write(first)
		_ = http.NewResponseController(w).Flush()
		_, _ = io.Copy(w, r.Body)
	}))
	defer backend.Close()
	h, f := setup(t)
	h.Target, _ = url.Parse(backend.URL)
	h.Transport = backend.Client().Transport
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.Handle(w, r) }))
	defer front.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	req, _ := http.NewRequestWithContext(ctx, "POST", front.URL+"/process.Process/StreamInput", reader)
	req.Host = "49983-sandbox.sandbox.example"
	req.Header.Set("X-Access-Token", h.Tokens.Token(f.b, "envd"))
	req.Header.Set("Content-Type", "application/connect+proto")
	firstWritten := make(chan error, 1)
	go func() { _, err := writer.Write([]byte("a")); firstWritten <- err }()
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 1)
	if _, err = io.ReadFull(resp.Body, first); err != nil || string(first) != "a" {
		t.Fatal("response did not stream before input EOF", err)
	}
	if err = <-firstWritten; err != nil {
		t.Fatal(err)
	}
	go func() { writer.Write([]byte("b")); writer.Close() }()
	tail, err := io.ReadAll(resp.Body)
	if err != nil || string(tail) != "b" {
		t.Fatal("remaining stream lost", string(tail), err)
	}
}
func TestProxyWebSocketUpgrade(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			t.Error("upgrade lost")
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		rw.Flush()
		payload := make([]byte, 4)
		if _, err = io.ReadFull(rw, payload); err == nil {
			rw.Write(payload)
			rw.Flush()
		}
	}))
	defer backend.Close()
	h, f := setup(t)
	h.Target, _ = url.Parse(backend.URL)
	h.Transport = backend.Client().Transport
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.Handle(w, r) }))
	defer front.Close()
	u, _ := url.Parse(front.URL)
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /terminal HTTP/1.1\r\nHost: 49983-sandbox.sandbox.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nX-Access-Token: %s\r\n\r\n", h.Tokens.Token(f.b, "envd"))
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade failed %v %v", resp, err)
	}
	_, _ = io.WriteString(conn, "ping")
	payload := make([]byte, 4)
	if _, err = io.ReadFull(reader, payload); err != nil || strings.TrimSpace(string(payload)) != "ping" {
		t.Fatal("upgraded stream failed", err)
	}
}
