package ssh

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Client wraps an SSH client connection.
type Client struct {
	GoClient *ssh.Client
	mu       sync.Mutex
	probing  bool
}

// AuthConfig holds authentication parameters.
type AuthConfig struct {
	Password string
	KeyPath  string
}

// Connect establishes an SSH connection using the given auth config.
//
// Auth method order mirrors user intent:
//  1. explicit password (AuthConfig.Password) — password + keyboard-interactive
//  2. explicit key (AuthConfig.KeyPath)
//  3. default key material when no explicit key: ssh-agent (SSH_AUTH_SOCK),
//     then the standard ~/.ssh default key files
//
// Password goes first when explicitly provided: offering a dozen agent keys
// before the password can trip the server's MaxAuthTries limit.
func Connect(user, host string, port int, auth AuthConfig) (*Client, error) {
	var authMethods []ssh.AuthMethod

	if auth.Password != "" {
		authMethods = append(authMethods, ssh.Password(auth.Password))
		handler := &keyboardInteractiveHandler{Password: auth.Password}
		authMethods = append(authMethods, ssh.KeyboardInteractive(handler.Challenge))
	}

	var agentConn net.Conn
	if auth.KeyPath != "" {
		methods, err := authFromKeyPath(auth.KeyPath)
		if err != nil {
			return nil, err
		}
		authMethods = append(authMethods, methods...)
	} else {
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			if conn, err := net.Dial("unix", sock); err == nil {
				agentConn = conn
				authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
			}
		}
		authMethods = append(authMethods, defaultKeyAuthMethods()...)
	}
	if agentConn != nil {
		// The agent connection is only needed during the auth handshake.
		defer agentConn.Close()
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no authentication method available: pass --pswd or -i, load a key into ssh-agent, or add a default key (~/.ssh/id_ed25519)")
	}

	hostKeyCallback, err := getHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("failed to get host key callback: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	goClient, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	return &Client{GoClient: goClient}, nil
}

// Close closes the SSH connection.
func (c *Client) Close() error {
	c.mu.Lock()
	client := c.GoClient
	c.GoClient = nil
	c.probing = false
	c.mu.Unlock()

	if client != nil {
		return client.Close()
	}
	return nil
}

// IsAlive checks if the SSH connection is still alive.
func (c *Client) IsAlive() bool {
	c.mu.Lock()
	if c.GoClient == nil {
		c.mu.Unlock()
		return false
	}
	if c.probing {
		c.mu.Unlock()
		return false
	}
	client := c.GoClient
	c.probing = true
	c.mu.Unlock()

	aliveCh := make(chan bool, 1)
	go func() {
		_, _, err := client.SendRequest("keepalive@gssh", true, nil)
		c.mu.Lock()
		c.probing = false
		c.mu.Unlock()
		aliveCh <- (err == nil)
	}()
	select {
	case alive := <-aliveCh:
		return alive
	case <-time.After(5 * time.Second):
		_ = c.Close()
		return false
	}
}

// --- Auth helpers ---

type keyboardInteractiveHandler struct {
	Password string
}

func (k *keyboardInteractiveHandler) Challenge(name, instruction string, questions []string, echoes []bool) ([]string, error) {
	answers := make([]string, len(questions))
	for i := range questions {
		answers[i] = k.Password
	}
	return answers, nil
}

func authFromKeyPath(keyPath string) ([]ssh.AuthMethod, error) {
	keyPath = expandPath(keyPath)
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		var passErr *ssh.PassphraseMissingError
		if errors.As(err, &passErr) {
			return nil, fmt.Errorf("key %s is passphrase-protected: load it into ssh-agent (ssh-add) first; passphrase prompt is not supported", keyPath)
		}
		return nil, fmt.Errorf("unable to parse private key: %w", err)
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
}

// defaultKeyAuthMethods loads the standard ~/.ssh default keys that exist
// and are usable without a passphrase (same set ssh tries by default).
// Passphrase-protected keys are skipped — they belong in ssh-agent.
func defaultKeyAuthMethods() []ssh.AuthMethod {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	var methods []ssh.AuthMethod
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa", "id_dsa"} {
		key, err := os.ReadFile(filepath.Join(homeDir, ".ssh", name))
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			continue
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	return methods
}

// --- TOFU host key policy ---

func getHostKeyCallback() (ssh.HostKeyCallback, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not get user home dir: %w", err)
	}

	knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")

	// Ensure .ssh directory exists
	if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create .ssh directory: %w", err)
	}

	// Create known_hosts if it doesn't exist
	if _, err := os.Stat(knownHostsPath); os.IsNotExist(err) {
		f, err := os.OpenFile(knownHostsPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("failed to create known_hosts file: %w", err)
		}
		f.Close()
	}

	cb, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create knownhosts callback: %w", err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		if err == nil {
			return nil
		}

		keyErr, ok := err.(*knownhosts.KeyError)
		if !ok {
			return err
		}

		// Unknown key (len(Want) == 0): Trust-On-First-Use — auto-accept
		if len(keyErr.Want) == 0 {
			autoTrust := strings.EqualFold(os.Getenv("GSSH_INSECURE_ACCEPT_NEW_HOST_KEYS"), "1") ||
				strings.EqualFold(os.Getenv("GSSH_INSECURE_ACCEPT_NEW_HOST_KEYS"), "true")
			if !autoTrust {
				return fmt.Errorf(
					"unknown host key for %s (%s), refusing auto-trust; add it to %s or set GSSH_INSECURE_ACCEPT_NEW_HOST_KEYS=1",
					hostname,
					ssh.FingerprintSHA256(key),
					knownHostsPath,
				)
			}

			f, fErr := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_WRONLY, 0600)
			if fErr != nil {
				return fmt.Errorf("failed to open known_hosts for appending: %w", fErr)
			}
			defer f.Close()

			addresses := []string{knownhosts.Normalize(hostname)}
			if remoteString := remote.String(); remoteString != hostname {
				addresses = append(addresses, knownhosts.Normalize(remoteString))
			}

			line := knownhosts.Line(addresses, key)
			if _, wErr := f.WriteString(line + "\n"); wErr != nil {
				return fmt.Errorf("failed to append key to known_hosts: %w", wErr)
			}
			return nil
		}

		// Key mismatch: reject (possible MITM)
		return err
	}, nil
}

func expandPath(path string) string {
	if len(path) > 0 && path[0] == '~' {
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, path[1:])
	}
	return path
}
