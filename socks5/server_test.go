package socks5

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/proxy"
)

// How to test
// curl --socks5 localhost:3030 https://api.ipify.org

// TODO: prettify test, reuse code from application_test.go
func TestProxy(t *testing.T) {
	const listenAddr = "localhost:8673"
	socksServer := NewServer()
	socksClient, err := NewClient(listenAddr)
	require.NoError(t, err)
	go func() {
		conn := <-socksClient.ConnsChan()
		socksServer.ServeConn(conn)
	}()

	dialer, err := proxy.SOCKS5("tcp", listenAddr, nil, nil)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "test text")
	})
	go func() {
		// TODO: handle properly
		_ = http.ListenAndServe(":3030", mux)
	}()
	httpTransport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext}
	httpClient := http.Client{Transport: httpTransport}

	response, err := httpClient.Get("http://localhost:3030/test")
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	require.Equal(t, "test text", string(body))
}
