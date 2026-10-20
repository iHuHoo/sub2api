package service

import (
	"bufio"
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Explicit opt-in to a pinned local client; never downloads binaries or uses real proxies.
func TestClashSubscriptionMihomoRemoteDNS(t *testing.T) {
	binary := os.Getenv("CLASH_MIHOMO_BIN")
	if binary == "" {
		t.Skip("set CLASH_MIHOMO_BIN for isolated client acceptance")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	target := make(chan string, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		header := make([]byte, 2)
		if _, e = io.ReadFull(conn, header); e != nil {
			return
		}
		methods := make([]byte, int(header[1]))
		if _, e = io.ReadFull(conn, methods); e != nil {
			return
		}
		if _, e = conn.Write([]byte{5, 0}); e != nil {
			return
		}
		req := make([]byte, 4)
		if _, e = io.ReadFull(conn, req); e != nil {
			return
		}
		if req[3] != 3 {
			target <- fmt.Sprintf("unexpected ATYP %d", req[3])
			return
		}
		size := make([]byte, 1)
		if _, e = io.ReadFull(conn, size); e != nil {
			return
		}
		name := make([]byte, int(size[0]))
		if _, e = io.ReadFull(conn, name); e != nil {
			return
		}
		port := make([]byte, 2)
		if _, e = io.ReadFull(conn, port); e != nil {
			return
		}
		target <- string(name)
		if _, e = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80}); e != nil {
			return
		}
		if _, e = http.ReadRequest(bufio.NewReader(conn)); e != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nOK")
	}()
	dir := t.TempDir()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	set := FilterClashProxies([]Proxy{{ID: 1, Name: "fixture", Protocol: "socks5h", Host: "127.0.0.1", Port: port}})
	for _, s := range []ClashExportSet{set, FilterClashProxies(nil)} {
		data, e := RenderClashConfig(s)
		require.NoError(t, e)
		file := filepath.Join(dir, "validate.yaml")
		require.NoError(t, os.WriteFile(file, data, 0600))
		out, e := exec.Command(binary, "-d", dir, "-t", "-f", file).CombinedOutput()
		require.NoError(t, e, string(out))
	}
	local, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	localAddr := local.Addr().String()
	_, localPort, err := net.SplitHostPort(localAddr)
	require.NoError(t, err)
	require.NoError(t, local.Close())
	data, err := RenderClashConfig(set)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	n, err := strconv.Atoi(localPort)
	require.NoError(t, err)
	cfg["mixed-port"] = n
	data, err = yaml.Marshal(cfg)
	require.NoError(t, err)
	file := filepath.Join(dir, "runtime.yaml")
	require.NoError(t, os.WriteFile(file, data, 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-d", dir, "-f", file)
	require.NoError(t, cmd.Start())
	defer func() { cancel(); _ = cmd.Wait() }()
	require.Eventually(t, func() bool {
		c, e := net.DialTimeout("tcp", localAddr, 50*time.Millisecond)
		if e == nil {
			_ = c.Close()
			return true
		}
		return false
	}, 5*time.Second, 25*time.Millisecond)
	proxyURL, err := url.Parse("http://" + localAddr)
	require.NoError(t, err)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	res, err := client.Get("http://remote-dns-fixture.invalid/test")
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	select {
	case name := <-target:
		require.Equal(t, "remote-dns-fixture.invalid", name)
	case <-ctx.Done():
		t.Fatal("SOCKS destination missing")
	}
}
