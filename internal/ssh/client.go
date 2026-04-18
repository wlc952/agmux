package ssh

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Client wraps an SSH client connection.
type Client struct {
	GoClient *ssh.Client
}

// AuthConfig holds authentication parameters.
type AuthConfig struct {
	Password string
	KeyPath  string
}

// Connect establishes an SSH connection using the given auth config.
func Connect(user, host string, port int, auth AuthConfig) (*Client, error) {
	var authMethods []ssh.AuthMethod

	if auth.KeyPath != "" {
		methods, err := authFromKeyPath(auth.KeyPath)
		if err != nil {
			return nil, err
		}
		authMethods = append(authMethods, methods...)
	}

	if auth.Password != "" {
		authMethods = append(authMethods, ssh.Password(auth.Password))
		handler := &keyboardInteractiveHandler{Password: auth.Password}
		authMethods = append(authMethods, ssh.KeyboardInteractive(handler.Challenge))
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no authentication method provided")
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

	addr := fmt.Sprintf("%s:%d", host, port)
	goClient, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	return &Client{GoClient: goClient}, nil
}

// Close closes the SSH connection.
func (c *Client) Close() error {
	if c.GoClient != nil {
		return c.GoClient.Close()
	}
	return nil
}

// IsAlive checks if the SSH connection is still alive.
func (c *Client) IsAlive() bool {
	if c.GoClient == nil {
		return false
	}
	// SendRequest with wantReply=true can block on a dead connection.
	// Run in goroutine with timeout.
	aliveCh := make(chan bool, 1)
	go func() {
		_, _, err := c.GoClient.SendRequest("keepalive@agmux", true, nil)
		aliveCh <- (err == nil)
	}()
	select {
	case alive := <-aliveCh:
		return alive
	case <-time.After(5 * time.Second):
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
		return nil, fmt.Errorf("unable to parse private key: %w", err)
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
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