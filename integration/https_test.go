package integration

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/containous/traefik/integration/try"
	traefikTls "github.com/containous/traefik/tls"
	"github.com/containous/traefik/types"
	"github.com/go-check/check"
	checker "github.com/vdemeester/shakers"
)

// HTTPSSuite
type HTTPSSuite struct{ BaseSuite }

// TestWithSNIConfigHandshake involves a client sending a SNI hostname of
// "snitest.com", which happens to match the CN of 'snitest.com.crt'. The test
// verifies that traefik presents the correct certificate.
func (s *HTTPSSuite) TestWithSNIConfigHandshake(c *check.C) {
	cmd, display := s.traefikCmd(withConfigFile("fixtures/https/https_sni.toml"))
	defer display(c)
	err := cmd.Start()
	c.Assert(err, checker.IsNil)
	defer cmd.Process.Kill()

	// wait for Traefik
	err = try.GetRequest("http://127.0.0.1:8080/api/providers", 500*time.Millisecond, try.BodyContains("Host:snitest.org"))
	c.Assert(err, checker.IsNil)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "snitest.com",
		NextProtos:         []string{"h2", "http/1.1"},
	}
	conn, err := tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.IsNil, check.Commentf("failed to connect to server"))

	defer conn.Close()
	err = conn.Handshake()
	c.Assert(err, checker.IsNil, check.Commentf("TLS handshake error"))

	cs := conn.ConnectionState()
	err = cs.PeerCertificates[0].VerifyHostname("snitest.com")
	c.Assert(err, checker.IsNil, check.Commentf("certificate did not match SNI servername"))

	proto := conn.ConnectionState().NegotiatedProtocol
	c.Assert(proto, checker.Equals, "h2")
}

// TestWithSNIConfigRoute involves a client sending HTTPS requests with
// SNI hostnames of "snitest.org" and "snitest.com". The test verifies
// that traefik routes the requests to the expected backends.
func (s *HTTPSSuite) TestWithSNIConfigRoute(c *check.C) {
	cmd, display := s.traefikCmd(withConfigFile("fixtures/https/https_sni.toml"))
	defer display(c)
	err := cmd.Start()
	c.Assert(err, checker.IsNil)
	defer cmd.Process.Kill()

	// wait for Traefik
	err = try.GetRequest("http://127.0.0.1:8080/api/providers", 500*time.Millisecond, try.BodyContains("Host:snitest.org"))
	c.Assert(err, checker.IsNil)

	backend1 := startTestServer("9010", http.StatusNoContent)
	backend2 := startTestServer("9020", http.StatusResetContent)
	defer backend1.Close()
	defer backend2.Close()

	err = try.GetRequest(backend1.URL, 1*time.Second, try.StatusCodeIs(http.StatusNoContent))
	c.Assert(err, checker.IsNil)
	err = try.GetRequest(backend2.URL, 1*time.Second, try.StatusCodeIs(http.StatusResetContent))
	c.Assert(err, checker.IsNil)

	tr1 := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "snitest.com",
		},
	}
	tr2 := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "snitest.org",
		},
	}

	client := &http.Client{Transport: tr1}
	req, err := http.NewRequest(http.MethodGet, "https://127.0.0.1:4443/", nil)
	c.Assert(err, checker.IsNil)
	req.Host = "snitest.com"
	req.Header.Set("Host", "snitest.com")
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	c.Assert(err, checker.IsNil)
	// Expected a 204 (from backend1)
	c.Assert(resp.StatusCode, checker.Equals, http.StatusNoContent)

	client = &http.Client{Transport: tr2}
	req, err = http.NewRequest(http.MethodGet, "https://127.0.0.1:4443/", nil)
	c.Assert(err, checker.IsNil)
	req.Host = "snitest.org"
	req.Header.Set("Host", "snitest.org")
	req.Header.Set("Accept", "*/*")
	resp, err = client.Do(req)
	c.Assert(err, checker.IsNil)
	// Expected a 205 (from backend2)
	c.Assert(resp.StatusCode, checker.Equals, http.StatusResetContent)
}

// TestWithClientCertificateAuthentication
// The client can send a certificate signed by a CA trusted by the server but it's optional
func (s *HTTPSSuite) TestWithClientCertificateAuthentication(c *check.C) {
	cmd, display := s.traefikCmd(withConfigFile("fixtures/https/clientca/https_1ca1config.toml"))
	defer display(c)
	err := cmd.Start()
	c.Assert(err, checker.IsNil)
	defer cmd.Process.Kill()

	// wait for Traefik
	err = try.GetRequest("http://127.0.0.1:8080/api/providers", 500*time.Millisecond, try.BodyContains("Host:snitest.org"))
	c.Assert(err, checker.IsNil)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "snitest.com",
		Certificates:       []tls.Certificate{},
	}
	// Connection without client certificate should fail
	_, err = tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.IsNil, check.Commentf("should be allowed to connect to server"))

	// Connect with client certificate signed by ca1
	cert, err := tls.LoadX509KeyPair("fixtures/https/clientca/client1.crt", "fixtures/https/clientca/client1.key")
	c.Assert(err, checker.IsNil, check.Commentf("unable to load client certificate and key"))
	tlsConfig.Certificates = append(tlsConfig.Certificates, cert)

	conn, err := tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.IsNil, check.Commentf("failed to connect to server"))

	conn.Close()

	// Connect with client certificate not signed by ca1
	cert, err = tls.LoadX509KeyPair("fixtures/https/snitest.org.cert", "fixtures/https/snitest.org.key")
	c.Assert(err, checker.IsNil, check.Commentf("unable to load client certificate and key"))
	tlsConfig.Certificates = append(tlsConfig.Certificates, cert)

	conn, err = tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.IsNil, check.Commentf("failed to connect to server"))

	conn.Close()

	// Connect with client signed by ca2 should fail
	tlsConfig = &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "snitest.com",
		Certificates:       []tls.Certificate{},
	}
	cert, err = tls.LoadX509KeyPair("fixtures/https/clientca/client2.crt", "fixtures/https/clientca/client2.key")
	c.Assert(err, checker.IsNil, check.Commentf("unable to load client certificate and key"))
	tlsConfig.Certificates = append(tlsConfig.Certificates, cert)

	_, err = tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.IsNil, check.Commentf("should be allowed to connect to server"))
}

// TestWithClientCertificateAuthentication
// Use two CA:s and test that clients with client signed by either of them can connect
func (s *HTTPSSuite) TestWithClientCertificateAuthenticationMultipeCAs(c *check.C) {
	cmd, display := s.traefikCmd(withConfigFile("fixtures/https/clientca/https_2ca1config.toml"))
	defer display(c)
	err := cmd.Start()
	c.Assert(err, checker.IsNil)
	defer cmd.Process.Kill()

	// wait for Traefik
	err = try.GetRequest("http://127.0.0.1:8080/api/providers", 500*time.Millisecond, try.BodyContains("Host:snitest.org"))
	c.Assert(err, checker.IsNil)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "snitest.com",
		Certificates:       []tls.Certificate{},
	}
	// Connection without client certificate should fail
	_, err = tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.NotNil, check.Commentf("should not be allowed to connect to server"))

	// Connect with client signed by ca1
	cert, err := tls.LoadX509KeyPair("fixtures/https/clientca/client1.crt", "fixtures/https/clientca/client1.key")
	c.Assert(err, checker.IsNil, check.Commentf("unable to load client certificate and key"))
	tlsConfig.Certificates = append(tlsConfig.Certificates, cert)

	conn, err := tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.IsNil, check.Commentf("failed to connect to server"))

	conn.Close()

	// Connect with client signed by ca2
	tlsConfig = &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "snitest.com",
		Certificates:       []tls.Certificate{},
	}
	cert, err = tls.LoadX509KeyPair("fixtures/https/clientca/client2.crt", "fixtures/https/clientca/client2.key")
	c.Assert(err, checker.IsNil, check.Commentf("unable to load client certificate and key"))
	tlsConfig.Certificates = append(tlsConfig.Certificates, cert)

	conn, err = tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.IsNil, check.Commentf("failed to connect to server"))

	conn.Close()

	// Connect with client signed by ca3 should fail
	tlsConfig = &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "snitest.com",
		Certificates:       []tls.Certificate{},
	}
	cert, err = tls.LoadX509KeyPair("fixtures/https/clientca/client3.crt", "fixtures/https/clientca/client3.key")
	c.Assert(err, checker.IsNil, check.Commentf("unable to load client certificate and key"))
	tlsConfig.Certificates = append(tlsConfig.Certificates, cert)

	_, err = tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.NotNil, check.Commentf("should not be allowed to connect to server"))
}

// TestWithClientCertificateAuthentication
// Use two CA:s in two different files and test that clients with client signed by either of them can connect
func (s *HTTPSSuite) TestWithClientCertificateAuthenticationMultipeCAsMultipleFiles(c *check.C) {
	cmd, display := s.traefikCmd(withConfigFile("fixtures/https/clientca/https_2ca2config.toml"))
	defer display(c)
	err := cmd.Start()
	c.Assert(err, checker.IsNil)
	defer cmd.Process.Kill()

	// wait for Traefik
	err = try.GetRequest("http://127.0.0.1:8080/api/providers", 1000*time.Millisecond, try.BodyContains("Host:snitest.org"))
	c.Assert(err, checker.IsNil)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "snitest.com",
		Certificates:       []tls.Certificate{},
	}
	// Connection without client certificate should fail
	_, err = tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.NotNil, check.Commentf("should not be allowed to connect to server"))

	// Connect with client signed by ca1
	cert, err := tls.LoadX509KeyPair("fixtures/https/clientca/client1.crt", "fixtures/https/clientca/client1.key")
	c.Assert(err, checker.IsNil, check.Commentf("unable to load client certificate and key"))
	tlsConfig.Certificates = append(tlsConfig.Certificates, cert)

	conn, err := tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.IsNil, check.Commentf("failed to connect to server"))

	conn.Close()

	// Connect with client signed by ca2
	tlsConfig = &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "snitest.com",
		Certificates:       []tls.Certificate{},
	}
	cert, err = tls.LoadX509KeyPair("fixtures/https/clientca/client2.crt", "fixtures/https/clientca/client2.key")
	c.Assert(err, checker.IsNil, check.Commentf("unable to load client certificate and key"))
	tlsConfig.Certificates = append(tlsConfig.Certificates, cert)

	conn, err = tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.IsNil, check.Commentf("failed to connect to server"))
	conn.Close()

	// Connect with client signed by ca3 should fail
	tlsConfig = &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "snitest.com",
		Certificates:       []tls.Certificate{},
	}
	cert, err = tls.LoadX509KeyPair("fixtures/https/clientca/client3.crt", "fixtures/https/clientca/client3.key")
	c.Assert(err, checker.IsNil, check.Commentf("unable to load client certificate and key"))
	tlsConfig.Certificates = append(tlsConfig.Certificates, cert)

	_, err = tls.Dial("tcp", "127.0.0.1:4443", tlsConfig)
	c.Assert(err, checker.NotNil, check.Commentf("should not be allowed to connect to server"))
}

func (s *HTTPSSuite) TestWithRootCAsContentForHTTPSOnBackend(c *check.C) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	file := s.adaptFile(c, "fixtures/https/rootcas/https.toml", struct{ BackendHost string }{backend.URL})
	defer os.Remove(file)
	cmd, display := s.traefikCmd(withConfigFile(file))
	defer display(c)
	err := cmd.Start()
	c.Assert(err, checker.IsNil)
	defer cmd.Process.Kill()

	// wait for Traefik
	err = try.GetRequest("http://127.0.0.1:8080/api/providers", 1*time.Second, try.BodyContains(backend.URL))
	c.Assert(err, checker.IsNil)

	err = try.GetRequest("http://127.0.0.1:8081/ping", 1*time.Second, try.StatusCodeIs(http.StatusOK))
	c.Assert(err, checker.IsNil)
}

func (s *HTTPSSuite) TestWithRootCAsFileForHTTPSOnBackend(c *check.C) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	file := s.adaptFile(c, "fixtures/https/rootcas/https_with_file.toml", struct{ BackendHost string }{backend.URL})
	defer os.Remove(file)
	cmd, display := s.traefikCmd(withConfigFile(file))
	defer display(c)
	err := cmd.Start()
	c.Assert(err, checker.IsNil)
	defer cmd.Process.Kill()

	// wait for Traefik
	err = try.GetRequest("http://127.0.0.1:8080/api/providers", 1*time.Second, try.BodyContains(backend.URL))
	c.Assert(err, checker.IsNil)

	err = try.GetRequest("http://127.0.0.1:8081/ping", 1*time.Second, try.StatusCodeIs(http.StatusOK))
	c.Assert(err, checker.IsNil)
}

func startTestServer(port string, statusCode int) (ts *httptest.Server) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		panic(err)
	}

	ts = &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	ts.Start()
	return ts
}

// TestWithSNIDynamicConfigRouteWithNoChange involves a client sending HTTPS requests with
// SNI hostnames of "snitest.org" and "snitest.com". The test verifies
// that traefik routes the requests to the expected backends thanks to given certificate if possible
// otherwise thanks to the default one.
func (s *HTTPSSuite) TestWithSNIDynamicConfigRouteWithNoChange(c *check.C) {
	dynamicConfFileName := s.adaptFile(c, "fixtures/https/dynamic_https.toml", struct{}{})
	defer os.Remove(dynamicConfFileName)
	confFileName := s.adaptFile(c, "fixtures/https/dynamic_https_sni.toml", struct {
		DynamicConfFileName string
	}{
		DynamicConfFileName: dynamicConfFileName,
	})
	defer os.Remove(confFileName)
	cmd, display := s.traefikCmd(withConfigFile(confFileName))
	defer display(c)
	err := cmd.Start()
	c.Assert(err, checker.IsNil)
	defer cmd.Process.Kill()

	tr1 := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "snitest.org",
		},
	}

	tr2 := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "snitest.com",
		},
	}

	// wait for Traefik
	err = try.GetRequest("http://127.0.0.1:8080/api/providers", 1*time.Second, try.BodyContains("Host:"+tr1.TLSClientConfig.ServerName))
	c.Assert(err, checker.IsNil)

	backend1 := startTestServer("9010", http.StatusNoContent)
	backend2 := startTestServer("9020", http.StatusResetContent)
	defer backend1.Close()
	defer backend2.Close()

	err = try.GetRequest(backend1.URL, 500*time.Millisecond, try.StatusCodeIs(http.StatusNoContent))
	c.Assert(err, checker.IsNil)
	err = try.GetRequest(backend2.URL, 500*time.Millisecond, try.StatusCodeIs(http.StatusResetContent))
	c.Assert(err, checker.IsNil)

	client := &http.Client{Transport: tr1}
	req, err := http.NewRequest(http.MethodGet, "https://127.0.0.1:4443/", nil)
	c.Assert(err, checker.IsNil)
	req.Host = tr1.TLSClientConfig.ServerName
	req.Header.Set("Host", tr1.TLSClientConfig.ServerName)
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	c.Assert(err, checker.IsNil)
	// snitest.org certificate must be used yet
	c.Assert(resp.TLS.PeerCertificates[0].Subject.CommonName, check.Equals, tr1.TLSClientConfig.ServerName)
	// Expected a 204 (from backend1)
	c.Assert(resp.StatusCode, checker.Equals, http.StatusResetContent)

	client = &http.Client{Transport: tr2}
	req.Host = tr2.TLSClientConfig.ServerName
	req.Header.Set("Host", tr2.TLSClientConfig.ServerName)
	resp, err = client.Do(req)
	c.Assert(err, checker.IsNil)
	// snitest.com certificate does not exist, default certificate has to be used
	c.Assert(resp.TLS.PeerCertificates[0].Subject.CommonName, checker.Not(check.Equals), tr2.TLSClientConfig.ServerName)
	// Expected a 205 (from backend2)
	c.Assert(resp.StatusCode, checker.Equals, http.StatusNoContent)
}

// TestWithSNIDynamicConfigRouteWithChange involves a client sending HTTPS requests with
// SNI hostnames of "snitest.org" and "snitest.com". The test verifies
// that traefik updates its configuration when the HTTPS configuration is modified and
// it routes the requests to the expected backends thanks to given certificate if possible
// otherwise thanks to the default one.
func (s *HTTPSSuite) TestWithSNIDynamicConfigRouteWithChange(c *check.C) {
	dynamicConfFileName := s.adaptFile(c, "fixtures/https/dynamic_https.toml", struct{}{})
	defer os.Remove(dynamicConfFileName)
	confFileName := s.adaptFile(c, "fixtures/https/dynamic_https_sni.toml", struct {
		DynamicConfFileName string
	}{
		DynamicConfFileName: dynamicConfFileName,
	})
	defer os.Remove(confFileName)
	cmd, display := s.traefikCmd(withConfigFile(confFileName))
	defer display(c)
	err := cmd.Start()
	c.Assert(err, checker.IsNil)
	defer cmd.Process.Kill()

	tr1 := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "snitest.com",
		},
	}

	tr2 := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "snitest.org",
		},
	}

	// wait for Traefik
	err = try.GetRequest("http://127.0.0.1:8080/api/providers", 1*time.Second, try.BodyContains("Host:"+tr2.TLSClientConfig.ServerName))
	c.Assert(err, checker.IsNil)

	backend1 := startTestServer("9010", http.StatusNoContent)
	backend2 := startTestServer("9020", http.StatusResetContent)
	defer backend1.Close()
	defer backend2.Close()

	err = try.GetRequest(backend1.URL, 500*time.Millisecond, try.StatusCodeIs(http.StatusNoContent))
	c.Assert(err, checker.IsNil)
	err = try.GetRequest(backend2.URL, 500*time.Millisecond, try.StatusCodeIs(http.StatusResetContent))
	c.Assert(err, checker.IsNil)

	req, err := http.NewRequest(http.MethodGet, "https://127.0.0.1:4443/", nil)
	client := &http.Client{Transport: tr1}
	req.Host = tr1.TLSClientConfig.ServerName
	req.Header.Set("Host", tr1.TLSClientConfig.ServerName)
	req.Header.Set("Accept", "*/*")

	// Change certificates configuration file content
	modifyCertificateConfFileContent(c, tr1.TLSClientConfig.ServerName, dynamicConfFileName, "https")
	var resp *http.Response
	err = try.Do(30*time.Second, func() error {
		resp, err = client.Do(req)

		// /!\ If connection is not closed, SSLHandshake will only be done during the first trial /!\
		req.Close = true

		if err != nil {
			return err
		}

		cn := resp.TLS.PeerCertificates[0].Subject.CommonName
		if cn != tr1.TLSClientConfig.ServerName {
			return fmt.Errorf("domain %s found in place of %s", cn, tr1.TLSClientConfig.ServerName)
		}

		return nil
	})
	c.Assert(err, checker.IsNil)
	c.Assert(resp.StatusCode, checker.Equals, http.StatusNotFound)
	client = &http.Client{Transport: tr2}
	req.Host = tr2.TLSClientConfig.ServerName
	req.Header.Set("Host", tr2.TLSClientConfig.ServerName)

	err = try.Do(60*time.Second, func() error {
		resp, err = client.Do(req)

		// /!\ If connection is not closed, SSLHandshake will only be done during the first trial /!\
		req.Close = true

		if err != nil {
			return err
		}

		cn := resp.TLS.PeerCertificates[0].Subject.CommonName
		if cn == tr2.TLSClientConfig.ServerName {
			return fmt.Errorf("domain %s found in place of default one", tr2.TLSClientConfig.ServerName)
		}

		return nil
	})
	c.Assert(err, checker.IsNil)
	c.Assert(resp.StatusCode, checker.Equals, http.StatusNotFound)
}

// TestWithSNIDynamicConfigRouteWithTlsConfigurationDeletion involves a client sending HTTPS requests with
// SNI hostnames of "snitest.org" and "snitest.com". The test verifies
// that traefik updates its configuration when the HTTPS configuration is modified, even if it totally deleted, and
// it routes the requests to the expected backends thanks to given certificate if possible
// otherwise thanks to the default one.
func (s *HTTPSSuite) TestWithSNIDynamicConfigRouteWithTlsConfigurationDeletion(c *check.C) {
	dynamicConfFileName := s.adaptFile(c, "fixtures/https/dynamic_https.toml", struct{}{})
	defer os.Remove(dynamicConfFileName)
	confFileName := s.adaptFile(c, "fixtures/https/dynamic_https_sni.toml", struct {
		DynamicConfFileName string
	}{
		DynamicConfFileName: dynamicConfFileName,
	})
	defer os.Remove(confFileName)
	cmd, display := s.traefikCmd(withConfigFile(confFileName))
	defer display(c)
	err := cmd.Start()
	c.Assert(err, checker.IsNil)
	defer cmd.Process.Kill()

	tr2 := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "snitest.org",
		},
	}

	// wait for Traefik
	err = try.GetRequest("http://127.0.0.1:8080/api/providers", 1*time.Second, try.BodyContains("Host:"+tr2.TLSClientConfig.ServerName))
	c.Assert(err, checker.IsNil)

	backend2 := startTestServer("9020", http.StatusResetContent)

	defer backend2.Close()

	err = try.GetRequest(backend2.URL, 500*time.Millisecond, try.StatusCodeIs(http.StatusResetContent))
	c.Assert(err, checker.IsNil)

	req, err := http.NewRequest(http.MethodGet, "https://127.0.0.1:4443/", nil)
	client := &http.Client{Transport: tr2}
	req.Host = tr2.TLSClientConfig.ServerName
	req.Header.Set("Host", tr2.TLSClientConfig.ServerName)
	req.Header.Set("Accept", "*/*")

	var resp *http.Response
	err = try.Do(30*time.Second, func() error {
		resp, err = client.Do(req)

		// /!\ If connection is not closed, SSLHandshake will only be done during the first trial /!\
		req.Close = true

		if err != nil {
			return err
		}

		cn := resp.TLS.PeerCertificates[0].Subject.CommonName
		if cn != tr2.TLSClientConfig.ServerName {
			return fmt.Errorf("domain %s found in place of %s", cn, tr2.TLSClientConfig.ServerName)
		}

		return nil
	})
	c.Assert(err, checker.IsNil)
	c.Assert(resp.StatusCode, checker.Equals, http.StatusResetContent)
	// Change certificates configuration file content
	modifyCertificateConfFileContent(c, "", dynamicConfFileName, "https02")

	err = try.Do(60*time.Second, func() error {
		resp, err = client.Do(req)

		// /!\ If connection is not closed, SSLHandshake will only be done during the first trial /!\
		req.Close = true

		if err != nil {
			return err
		}

		cn := resp.TLS.PeerCertificates[0].Subject.CommonName
		if cn == tr2.TLSClientConfig.ServerName {
			return fmt.Errorf("domain %s found in place of default one", tr2.TLSClientConfig.ServerName)
		}

		return nil
	})
	c.Assert(err, checker.IsNil)
	c.Assert(resp.StatusCode, checker.Equals, http.StatusNotFound)
}

// modifyCertificateConfFileContent replaces the content of a HTTPS configuration file.
func modifyCertificateConfFileContent(c *check.C, certFileName, confFileName, entryPoint string) {
	f, err := os.OpenFile("./"+confFileName, os.O_WRONLY, os.ModeExclusive)
	c.Assert(err, checker.IsNil)
	defer func() {
		f.Close()
	}()
	f.Truncate(0)
	// If certificate file is not provided, just truncate the configuration file
	if len(certFileName) > 0 {
		tlsConf := types.Configuration{
			TLS: []*traefikTls.Configuration{
				{
					Certificate: &traefikTls.Certificate{
						CertFile: traefikTls.FileOrContent("fixtures/https/" + certFileName + ".cert"),
						KeyFile:  traefikTls.FileOrContent("fixtures/https/" + certFileName + ".key"),
					},
					EntryPoints: []string{entryPoint},
				},
			},
		}
		var confBuffer bytes.Buffer
		e := toml.NewEncoder(&confBuffer)
		err := e.Encode(tlsConf)
		c.Assert(err, checker.IsNil)

		_, err = f.Write(confBuffer.Bytes())
		c.Assert(err, checker.IsNil)
	}
}
