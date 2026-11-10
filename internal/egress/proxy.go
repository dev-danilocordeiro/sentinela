// Package egress é o proxy de saída do job em contêiner (ADR 0053): a rede do job não tem rota para
// fora, e a única saída é este proxy, que só aceita destinos de uma lista permitida.
package egress

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

// Allowlist decide se um host pode ser alcançado. Uma entrada "exemplo.com" vale só para o
// próprio host; ".exemplo.com" vale para os subdomínios (e para o próprio).
type Allowlist struct {
	exact  map[string]bool
	suffix []string
}

// NewAllowlist monta a lista a partir das entradas do sentinela.yaml.
func NewAllowlist(entries []string) Allowlist {
	a := Allowlist{exact: map[string]bool{}}
	for _, e := range entries {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
		case strings.HasPrefix(e, "."):
			a.suffix = append(a.suffix, e)
			a.exact[strings.TrimPrefix(e, ".")] = true
		default:
			a.exact[e] = true
		}
	}
	return a
}

// Allows diz se o host (sem porta) está na lista.
func (a Allowlist) Allows(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if a.exact[host] {
		return true
	}
	for _, s := range a.suffix {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

// Proxy é um proxy HTTP com CONNECT, restrito à lista permitida.
type Proxy struct {
	Allow Allowlist
	// Ports são as portas aceitas no CONNECT (padrão 443).
	Ports []string
	Log   *log.Logger
	Dial  func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (p *Proxy) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log.Printf(format, args...)
	}
}

func (p *Proxy) dial(ctx context.Context, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, "tcp", addr)
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	return d.DialContext(ctx, "tcp", addr)
}

func (p *Proxy) portAllowed(port string) bool {
	ports := p.Ports
	if len(ports) == 0 {
		ports = []string{"443"}
	}
	for _, q := range ports {
		if q == port {
			return true
		}
	}
	return false
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	p.forward(w, r)
}

// connect abre o túnel TLS para um host permitido.
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "destino inválido", http.StatusBadRequest)
		return
	}
	if !p.Allow.Allows(host) || !p.portAllowed(port) {
		p.logf("negado CONNECT %s", r.Host)
		http.Error(w, "destino fora da lista permitida do Sentinela (execution.container.egress_allow): "+r.Host, http.StatusForbidden)
		return
	}
	upstream, err := p.dial(r.Context(), r.Host)
	if err != nil {
		p.logf("falha CONNECT %s: %v", r.Host, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "sem hijack", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	p.logf("permitido CONNECT %s", r.Host)
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	var once sync.Once
	closeBoth := func() { _ = client.Close(); _ = upstream.Close() }
	go func() {
		// O que o cliente já mandou junto com o CONNECT segue antes do resto.
		if n := buf.Reader.Buffered(); n > 0 {
			b, _ := buf.Peek(n)
			_, _ = upstream.Write(b)
		}
		_, _ = io.Copy(upstream, client)
		once.Do(closeBoth)
	}()
	_, _ = io.Copy(client, upstream)
	once.Do(closeBoth)
}

// forward repassa um pedido HTTP comum (URL absoluta) para um host permitido.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL.Host == "" {
		http.Error(w, "o proxy do Sentinela só aceita CONNECT ou URL absoluta", http.StatusBadRequest)
		return
	}
	host := r.URL.Hostname()
	port := r.URL.Port()
	if port == "" {
		port = "80"
	}
	if !p.Allow.Allows(host) || (port != "80" && !p.portAllowed(port)) {
		p.logf("negado %s %s", r.Method, r.URL.Host)
		http.Error(w, "destino fora da lista permitida do Sentinela (execution.container.egress_allow): "+r.URL.Host, http.StatusForbidden)
		return
	}
	p.logf("permitido %s %s", r.Method, r.URL.Host)
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = r.URL
			pr.Out.Host = r.URL.Host
		},
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) { return p.dial(ctx, addr) }},
	}
	rp.ServeHTTP(w, r)
}

// Serve escuta em addr até o contexto acabar.
func Serve(ctx context.Context, addr string, p *Proxy) error {
	srv := &http.Server{Addr: addr, Handler: p, ReadHeaderTimeout: 15 * time.Second}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	p.logf("proxy de saída ouvindo em %s", ln.Addr())
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("proxy de saída: %w", err)
	}
	return nil
}
