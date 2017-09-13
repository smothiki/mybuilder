// Package sshd implements an SSH server.
//
// See https://tools.ietf.org/html/rfc4254
package main

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"strings"

	"github.com/smothiki/mybuilder/git"
	"golang.org/x/crypto/ssh"
)

const (
	// HostKeys is the context key for Host Keys list.
	HostKeys string = "ssh.HostKeys"
	// Address is the context key for SSH address.
	Address string = "ssh.Address"
	// ServerConfig is the context key for ServerConfig object.
	ServerConfig string = "ssh.ServerConfig"

	multiplePush string = "Another git push is ongoing"
)

var errBuildAppPerm = errors.New("user has no permission to build the app")
var errDirPerm = errors.New("Cannot change directory in file name.")
var errDirCreatePerm = errors.New("Empty repo name.")

// AuthKey authenticates based on a public key.
func AuthKey(key ssh.PublicKey) (*ssh.Permissions, error) {
	fmt.Println("Starting ssh authentication")

	perm := &ssh.Permissions{
		Extensions: map[string]string{
			"user":        "smothiki",
			"fingerprint": "nothing",
			"apps":        "apps",
		},
	}
	return perm, nil
}

// Configure creates a new SSH configuration object.
//
// Config sets a PublicKeyCallback handler that forwards public key auth
// requests to the route named "pubkeyAuth".
//
// This assumes certain details about our environment, like the location of the
// host keys. It also provides only key-based authentication.
// ConfigureServerSshConfig
//
// Returns:
//  An *ssh.ServerConfig
func Configure() (*ssh.ServerConfig, error) {
	cfg := &ssh.ServerConfig{
		// PublicKeyCallback: func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		// 	return nil, nil
		// },
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			// Should use constant-time compare (or better, salt+hash) in a production setting.
			return nil, nil

		},
	}
	path := "key"
	// for _, t := range hostKeyTypes {
	// 	path := fmt.Sprintf(pathTpl, t)

	key, err := ioutil.ReadFile(path)
	if err != nil {
		fmt.Printf("Failed to read key %s (skipping): %s\n", path, err)
		return nil, err
	}
	hk, err := ssh.ParsePrivateKey(key)
	if err != nil {
		fmt.Printf("Failed to parse host key %s (skipping): %s\n", path, err)
		return nil, err
	}
	fmt.Printf("Parsed host key %s.\n", path)
	cfg.AddHostKey(hk)
	cfg.Config.Ciphers = []string{"aes128-ctr", "aes192-ctr", "aes256-ctr", "aes128-gcm@openssh.com"}
	return cfg, nil
}

// Serve starts a native SSH server.
func Serve(
	cfg *ssh.ServerConfig,
	gitHomeDir string,
	addr, receivetype string) error {

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	srv := &server{
		gitHome:     gitHomeDir,
		receivetype: receivetype,
	}

	fmt.Printf("Listening on %s\n", addr)
	srv.listen(listener, cfg)

	return nil
}

// server is the struct that encapsulates the SSH server.
type server struct {
	gitHome     string
	receivetype string
}

// listen handles accepting and managing connections. However, since closer
// is len(1), it will not block the sender.
func (s *server) listen(l net.Listener, conf *ssh.ServerConfig) error {

	fmt.Println("Accepting new connections.")
	defer l.Close()

	for {
		conn, err := l.Accept()
		if err != nil {
			fmt.Printf("Error during Accept: %s\n", err)
			// We shut down the listener if Accept errors
			return err
		}
		go s.handleConn(conn, conf)
	}
}

// handleConn handles an individual client connection.
//
// It manages the connection, but passes channels on to `answer()`.
func (s *server) handleConn(conn net.Conn, conf *ssh.ServerConfig) {
	defer conn.Close()
	fmt.Println("Accepted connection.")
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, conf)
	if err != nil {
		// Handshake failure.
		fmt.Printf("Failed handshake: %s\n", err)
		return
	}

	// Discard global requests. We're only concerned with channels.
	go ssh.DiscardRequests(reqs)

	condata := sshConnection(conn)

	// Now we handle the channels.
	for incoming := range chans {
		fmt.Printf("Channel type: %s\n", incoming.ChannelType())
		if incoming.ChannelType() != "session" {
			incoming.Reject(ssh.UnknownChannelType, "Unknown channel type")
		}

		channel, req, err := incoming.Accept()
		if err != nil {
			// Should close request and move on.
			panic(err)
		}
		go s.answer(channel, req, condata, sshConn)
	}
	conn.Close()
}

// sshConnection generates the SSH_CONNECTION environment variable.
//
// This is untested on UNIX sockets.
func sshConnection(conn net.Conn) string {
	remote := conn.RemoteAddr().String()
	local := conn.LocalAddr().String()
	rhost, rport, _ := net.SplitHostPort(remote)
	lhost, lport, _ := net.SplitHostPort(local)

	return fmt.Sprintf("%s %s %s %s", rhost, rport, lhost, lport)
}

func sendExitStatus(status uint32, channel ssh.Channel) error {
	exit := struct{ Status uint32 }{uint32(0)}
	_, err := channel.SendRequest("exit-status", false, ssh.Marshal(exit))
	return err
}

// answer handles answering requests and channel requests
//
// Currently, an exec must be either "ping", "git-receive-pack" or
// "git-upload-pack". Anything else will result in a failure response. Right
// now, we leave the channel open on failure because it is unclear what the
// correct behavior for a failed exec is.
//
// Support for setting environment variables via `env` has been disabled.
func (s *server) answer(channel ssh.Channel, requests <-chan *ssh.Request, condata string, sshconn *ssh.ServerConn) error {
	defer channel.Close()

	// Answer all the requests on this connection.
	for req := range requests {
		ok := false

		switch req.Type {
		case "env":
			o := &EnvVar{}
			ssh.Unmarshal(req.Payload, o)
			fmt.Printf("Key='%s', Value='%s'\n", o.Name, o.Value)
			req.Reply(true, nil)
		case "exec":
			clean := cleanExec(req.Payload)
			parts := strings.SplitN(clean, " ", 2)
			switch parts[0] {
			case "ping":
				err := Ping(channel, req)
				if err != nil {
					fmt.Printf("Error pinging: %s\n", err)
				}
				return err
			case "git-receive-pack", "git-upload-pack":
				if len(parts) < 2 {
					fmt.Println("Expected two-part command.")
					req.Reply(ok, nil)
					break
				}
				repoName, err := cleanRepoName(parts[1])
				if err != nil {
					fmt.Printf("Illegal repo name: %s.\n", err)
					channel.Stderr().Write([]byte("No repo given"))
					return err
				}
				ferr := s.runReceive(req, sshconn, channel, repoName, parts, condata)
				err = ferr()
				if pktErr := gitPktLine(channel, fmt.Sprintf("ERR %v\n", multiplePush)); pktErr != nil {
					fmt.Printf("Failed to write to channel: %s\n", err)
				}
				var xs uint32

				sendExitStatus(xs, channel)

				return nil
			default:
				fmt.Printf("Illegal command is '%s'\n", clean)
				req.Reply(false, nil)
				return nil
			}

			if err := sendExitStatus(0, channel); err != nil {
				fmt.Printf("Failed to write exit status: %s", err)
			}
			return nil
		default:
			// We simply ignore all of the other cases and leave the
			// channel open to take additional requests.
			fmt.Printf("Received request of type %s\n", req.Type)
			req.Reply(false, nil)
		}
	}

	return nil
}

func (s *server) runReceive(
	req *ssh.Request,
	sshConn *ssh.ServerConn,
	channel ssh.Channel,
	repoName string,
	parts []string,
	connData string,
) func() error {
	return func() error {
		req.Reply(true, nil) // We processed. Yay.
		// if !strings.Contains(sshConn.Permissions.Extensions["apps"], repoName) {
		// 	return errBuildAppPerm
		// }
		repo := repoName + ".git"
		recvErr := git.Receive(
			repo,
			parts[0],
			s.gitHome,
			channel,
			"nothing",
			"smothiki",
			connData,
			s.receivetype,
		)

		return recvErr
	}
}

// ExecCmd is an SSH exec request.
type ExecCmd struct {
	Value string
}

// EnvVar is an SSH env request.
type EnvVar struct {
	Name  string
	Value string
}

// GenericMessage describes a simple string message, which is common in SSH.
type GenericMessage struct {
	Value string
}

// cleanExec cleans the exec string.
func cleanExec(pay []byte) string {
	e := &ExecCmd{}
	ssh.Unmarshal(pay, e)
	// TODO: Minimal escaping of values in command. There is probably a better
	// way of doing this.
	r := strings.NewReplacer("$", "", "`", "'")
	return r.Replace(e.Value)
}

// Ping handles a simple test SSH exec.
//
// Returns the string PONG and exit status 0.
//
// Params:
// 	- channel (ssh.Channel): The channel to respond on.
// 	- request (*ssh.Request): The request.
//
func Ping(channel ssh.Channel, req *ssh.Request) error {
	fmt.Println("PING")
	if _, err := channel.Write([]byte("pong")); err != nil {
		fmt.Printf("Failed to write to channel: %s\n", err)
	}
	sendExitStatus(0, channel)
	req.Reply(true, nil)
	return nil
}

// cleanRepoName cleans a repository name for a git-sh operation.
func cleanRepoName(name string) (string, error) {
	if len(name) == 0 {
		return name, errDirCreatePerm
	}
	if strings.Contains(name, "..") {
		return "", errDirPerm
	}
	name = strings.Replace(name, "'", "", -1)
	return strings.TrimPrefix(strings.TrimSuffix(name, ".git"), "/"), nil
}

// gitPktLine writes a line following the pkt-line git protocol. See https://github.com/git/git/blob/master/Documentation/technical/protocol-common.txt for the protocol and https://github.com/git/git/blob/master/Documentation/technical/pack-protocol.txt for its usage.
func gitPktLine(w io.Writer, s string) error {
	_, err := fmt.Fprintf(w, "%04x%s", len(s)+4, s)
	return err
}
