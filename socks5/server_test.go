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
	socksClient, err := NewClient(listenAddr)
	require.NoError(t, err)

	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()

		conn := <-socksClient.ConnsChan()
		socksServer.ServeConn(conn)
	}()

	upstreamAddr := pickFreeAddr(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "test text")
	})
	//nolint
	httpServer := &http.Server{Addr: upstreamAddr, Handler: mux}
	go func() {
		_ = httpServer.ListenAndServe()
	}()
	defer func() {
		httpServer.Shutdown(context.Background())
	}()

	httpTransport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return &url.URL{
				Scheme: "socks5",
				Host:   listenAddr,
			}, nil
		},
	}
	httpClient := http.Client{Transport: httpTransport}

	response, err := httpClient.Get(fmt.Sprintf("http://%s/test", upstreamAddr))
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	err = response.Body.Close()
	require.NoError(t, err)

	require.Equal(t, "test text", string(body))

	httpTransport.CloseIdleConnections()
	wg.Wait()
}

func pickFreeAddr(t testing.TB) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	return l.Addr().String()
}
