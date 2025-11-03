package main

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/yggdrasil-network/yggdrasil-go/src/admin"
	"github.com/yggdrasil-network/yggdrasil-go/src/core"

	zfg "github.com/chaindead/zerocfg"
	"github.com/chaindead/zerocfg/env"
	"github.com/chaindead/zerocfg/yaml"
)

var (
	configPath = zfg.Str("config", "/etc/yggdrasil/peerman.yaml", "path to yaml conf file", zfg.Alias("c"))
	loopTime   = zfg.Dur("looptime", 1*time.Minute, "cycle time for checking peers")
	endpoint   = zfg.Str("endpoint", "unix:///var/run/yggdrasil/yggdrasil.sock", "yggdrasil admin endpoint")
	routers    = zfg.Strs("routers", []string{}, "trusted router public keys")
	soloPeers  = zfg.Strs("peers", []string{}, "peer URIs to toggle")
)

func main() {
	os.Exit(run())
}

func run() int {
	logger := log.New(os.Stderr, "", log.Flags())

	defer func() int {
		if r := recover(); r != nil {
			logger.Println("panic caught:", r)
			return 1
		}
		return 0
	}()

	logger.Println("reading configuration from", *configPath)

	err := zfg.Parse(
		env.New(),
		yaml.New(configPath),
	)
	if err != nil {
		panic(err)
	}

	// Sanity checking: see if urls parse and that public keys are a set length.
	for _, key := range *routers {
		if len(key) != 64 {
			logger.Fatal("router public keys are expected to be exactly 64 characters long:", key)
		}
	}
	for _, uri := range *soloPeers {
		_, err := url.Parse(uri)
		if err != nil {
			logger.Fatal("could not parse peer uri:", uri)
		}
	}

	server, err := NewServer(*endpoint, logger)
	if err != nil {
		panic(err)
	}
	defer server.Shutdown()

	for {

		server.GetPeers()
		hasTrustedRouters := server.hasLocalPeers(*routers)

		for _, peerUri := range *soloPeers {
			server.SetPeer(peerUri, !hasTrustedRouters)
		}

		time.Sleep(*loopTime)
	}

}

type Server struct {
	conn   *net.Conn
	logger *log.Logger
	peers  *[]admin.PeerEntry
}

func NewServer(endpoint string, logger *log.Logger) (*Server, error) {

	var conn net.Conn

	u, err := url.Parse(endpoint)
	if err == nil {
		switch strings.ToLower(u.Scheme) {
		case "unix":
			logger.Println("connecting to UNIX socket", (endpoint)[7:])
			conn, err = net.Dial("unix", (endpoint)[7:])
		case "tcp":
			logger.Println("connecting to TCP socket", u.Host)
			conn, err = net.Dial("tcp", u.Host)
		default:
			logger.Println("unknown protocol or malformed address")
			err = errors.New("protocol not supported")
		}
	} else {
		logger.Println("connecting to TCP socket", u.Host)
		conn, err = net.Dial("tcp", endpoint)
	}
	if err != nil {
		return nil, err
	}
	logger.Println("connected")

	return &Server{
		conn:   &conn,
		logger: logger,
	}, nil
}
func (s *Server) Shutdown() {
	(*s.conn).Close()
}

func (s *Server) query(send *admin.AdminSocketRequest) *admin.AdminSocketResponse {
	decoder := json.NewDecoder(*s.conn)
	encoder := json.NewEncoder(*s.conn)

	// We're a daemon with a persistent connection, so all our requests are keepalive.
	send.KeepAlive = true

	recv := &admin.AdminSocketResponse{}
	if err := encoder.Encode(&send); err != nil {
		s.logger.Fatal("json error when encoding request", err.Error())
	}
	if err := decoder.Decode(&recv); err != nil {
		s.logger.Fatal("json error when decoding response", err.Error())
	}
	if recv.Status == "error" {
		if err := recv.Error; err != "" {
			// This one is permissible, it means we tried to remove a peer that wasn't defined,
			// or add a peer that is already there.
			if err != core.ErrLinkNotConfigured.Error() && err != core.ErrLinkAlreadyConfigured.Error() {
				s.logger.Fatalf("Admin socket returned an unhandled error: %s", err)
			}
		} else {
			s.logger.Fatal("Admin socket returned an error but didn't specify any error text")
		}
	}
	return recv
}

func (s *Server) GetPeers() {
	request := &admin.AdminSocketRequest{Name: "getpeers"}

	s.logger.Println("getting peers")
	recv := s.query(request)

	var resp admin.GetPeersResponse
	if err := json.Unmarshal(recv.Response, &resp); err != nil {
		s.logger.Fatal("json error when parsing peers", err.Error())
	}
	s.peers = &resp.Peers
}

func (s *Server) SetPeer(uri string, state bool) {
	var err error

	send := &admin.AdminSocketRequest{Name: "removepeer"}

	// This kind of request actually requires building arguments.

	if state {
		s.logger.Println("adding peer:", uri)
		send.Name = "addpeer"
	} else {
		// removepeer apparently wants an argument with no query string parameters.
		// We already parsed these before on startup, so no need to panic here.
		uri, _ = stripQuery(uri)

		// Check if the peer is in the connected peer list.
		// if not, don't bother the server.
		for _, peer := range *s.peers {
			if uri == peer.URI {
				return
			}
		}

		s.logger.Println("removing peer:", uri)
	}

	args := map[string]string{
		"uri": uri,
	}

	if send.Arguments, err = json.Marshal(args); err != nil {
		s.logger.Fatal("json error while building arguments", err.Error())
	}

	// If I understand the code of yggdrasilctl, these are actually blind,
	// i.e. if they don't error out they don't return anything.
	s.query(send)
}

func (s *Server) hasLocalPeers(keys []string) bool {
	// Now the issue is with determining if any of them are local peers,
	// because PeerEntry structure does not contain this information.
	// We can only take a guess and assume that peers connected with a
	// link-local address are what we want.
	for _, peer := range *s.peers {
		// Skip peers connections that are not actually up.
		if !peer.Up {
			continue
		}

		url, err := url.Parse(peer.URI)
		if err != nil {
			s.logger.Fatal("bogus url in server response:", peer.URI)
		}

		// Peer URIs that are not actually IP addresses are not autoconfigured anyway.
		ip, err := netip.ParseAddr(url.Hostname())
		if err != nil {
			continue
		}

		// It's a link-local address, so if the key matches, it's a trusted router.
		if ip.IsLinkLocalUnicast() && slices.Contains(keys, peer.PublicKey) {
			return true
		}
	}
	return false
}

func stripQuery(thatUrl string) (string, error) {
	parsedURL, err := url.Parse(thatUrl)
	if err != nil {
		return "", err
	}
	parsedURL.RawQuery = ""
	return parsedURL.String(), nil
}
