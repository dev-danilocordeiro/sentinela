package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAllowlistExactAndSuffix(t *testing.T) {
	a := NewAllowlist([]string{"api.anthropic.com", ".githubusercontent.com", " GitHub.com "})
	for host, want := range map[string]bool{
		"api.anthropic.com":                  true,
		"API.Anthropic.com.":                 true,
		"evil.api.anthropic.com":             false,
		"github.com":                         true,
		"objects.githubusercontent.com":      true,
		"githubusercontent.com":              true,
		"githubusercontent.com.evil.example": false,
		"example.com":                        false,
	} {
		if got := a.Allows(host); got != want {
			t.Errorf("%s: %v, quer %v", host, got, want)
		}
	}
}

// O CONNECT só abre túnel para host e porta da lista; o resto recebe 403 e fica no log.
func TestProxyConnectsOnlyToAllowedHosts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok do destino") }))
	defer upstream.Close()
	_, upPort, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	var logged strings.Builder
	p := &Proxy{
		Allow: NewAllowlist([]string{"permitido.example"}),
		Ports: []string{upPort},
		Log:   newLogger(&logged),
		// Os nomes do teste resolvem para o servidor local.
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(addr)
			var d net.Dialer
			return d.DialContext(ctx, network, "127.0.0.1:"+port)
		},
	}
	srv := httptest.NewServer(p)
	defer srv.Close()

	connect := func(target string) (int, string) {
		t.Helper()
		c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			return resp.StatusCode, ""
		}
		fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target)
		inner, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(inner.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := connect("permitido.example:" + upPort); code != 200 || body != "ok do destino" {
		t.Errorf("permitido: %d %q", code, body)
	}
	if code, _ := connect("fora.example:" + upPort); code != http.StatusForbidden {
		t.Errorf("host fora da lista: %d", code)
	}
	if code, _ := connect("permitido.example:22"); code != http.StatusForbidden {
		t.Errorf("porta fora da lista: %d", code)
	}
	// HTTP comum: mesmo critério.
	proxyURL, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get("http://fora.example/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET fora da lista: %d", resp.StatusCode)
	}
	if !strings.Contains(logged.String(), "negado CONNECT fora.example") || !strings.Contains(logged.String(), "permitido CONNECT permitido.example") {
		t.Errorf("log:\n%s", logged.String())
	}
}

func newLogger(w io.Writer) *log.Logger { return log.New(w, "", 0) }
