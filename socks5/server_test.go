package socks5

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// How to test
// curl --socks5 localhost:3030 https://api.ipify.org

func TestProxy(t *testing.T) {
	listenAddr := pickFreeAddr(t)
	socksServer := NewServer()
	socksServer.SetRules(NewRulePermitAll())
	socksClient, err := NewClient(listenAddr, "", "")
	require.NoError(t, err)

	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn := <-socksClient.ConnsChan()
		socksServer.ServeConn(conn)
	}()

	upstreamAddr := startUpstreamServer(t)
	httpClient, transport := newSOCKS5HttpClient(listenAddr, nil)

	response, err := httpClient.Get(fmt.Sprintf("http://%s/test", upstreamAddr))
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	err = response.Body.Close()
	require.NoError(t, err)

	require.Equal(t, "test text", string(body))

	transport.CloseIdleConnections()
	wg.Wait()
}

func TestProxyWithAuth(t *testing.T) {
	listenAddr := pickFreeAddr(t)
	socksServer := NewServer()
	socksServer.SetRules(NewRulePermitAll())
	socksClient, err := NewClient(listenAddr, "testuser", "testpass")
	require.NoError(t, err)

	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn := <-socksClient.ConnsChan()
		socksServer.ServeConn(conn)
	}()

	upstreamAddr := startUpstreamServer(t)
	httpClient, transport := newSOCKS5HttpClient(listenAddr, url.UserPassword("testuser", "testpass"))

	response, err := httpClient.Get(fmt.Sprintf("http://%s/test", upstreamAddr))
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	err = response.Body.Close()
	require.NoError(t, err)
	require.Equal(t, "test text", string(body))

	transport.CloseIdleConnections()
	wg.Wait()
}

func TestProxyWithAuthRejection(t *testing.T) {
	tests := []struct {
		name     string
		userinfo *url.Userinfo
	}{
		{"WrongPassword", url.UserPassword("testuser", "wrongpass")},
		{"NoCredentials", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listenAddr := pickFreeAddr(t)
			socksClient, err := NewClient(listenAddr, "testuser", "testpass")
			require.NoError(t, err)

			wg := &sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				conn := <-socksClient.ConnsChan()
				_ = socksClient.HandleLocalAuth(conn)
				conn.Close()
			}()

			upstreamAddr := startUpstreamServer(t)
			httpClient, transport := newSOCKS5HttpClient(listenAddr, tt.userinfo)

			resp, err := httpClient.Get(fmt.Sprintf("http://%s/test", upstreamAddr))
			if resp != nil {
				resp.Body.Close()
			}
			require.Error(t, err)

			transport.CloseIdleConnections()
			wg.Wait()
		})
	}
}

func pickFreeAddr(t testing.TB) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	return l.Addr().String()
}

// startUpstreamServer starts an HTTP server that responds with "test text" on /test.
func startUpstreamServer(t testing.TB) string {
	addr := pickFreeAddr(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "test text")
	})
	//nolint
	httpServer := &http.Server{Addr: addr, Handler: mux}
	go func() {
		_ = httpServer.ListenAndServe()
	}()
	t.Cleanup(func() {
		httpServer.Shutdown(context.Background())
	})
	return addr
}

// newSOCKS5HttpClient creates an HTTP client that routes through a SOCKS5 proxy.
// Pass nil userinfo for no auth credentials.
func newSOCKS5HttpClient(proxyAddr string, userinfo *url.Userinfo) (http.Client, *http.Transport) {
	transport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return &url.URL{
				Scheme: "socks5",
				User:   userinfo,
				Host:   proxyAddr,
			}, nil
		},
	}
	return http.Client{Transport: transport}, transport
}
