package engine

import (
	"errors"
	"strings"

	"skybuild/internal/osx"
	"skybuild/internal/secret"
)

// A machine's screen, seen and used from here.
//
// A Mac shares its screen with macOS's own Screen Sharing (a VNC server on port 5900). The
// app reaches that port through ssh, over the connection sky already has to the machine: no
// port is opened to the network and nothing is installed. Screen Sharing asks for the name
// and password of a user of that Mac; they are kept in this computer's keychain.

const screenPort = "5900"

// ScreenCommand is the command whose input and output are the machine's screen sharing
// connection (ssh -W: a plain pipe to the port on the machine itself).
func (e *Engine) ScreenCommand(machine string) (string, []string, error) {
	if IsLocal(machine) {
		return "", nil, errors.New("this is the computer you are looking at")
	}
	m, err := e.Machine(machine)
	if err != nil {
		return "", nil, err
	}
	t := e.Target(m)
	args := append(t.TunnelOptions(), "-W", "127.0.0.1:"+screenPort, t.Dest())
	return osx.Which("ssh"), args, nil
}

// ScreenLogin is the saved name and password for a machine's Screen Sharing. The name
// defaults to the user sky connects as.
func (e *Engine) ScreenLogin(machine string) (user, password string) {
	if v := secret.Get(secret.ScreenLoginPrefix + machine); v != "" {
		user, password, _ = strings.Cut(v, "\n")
	}
	if user == "" {
		if m, err := e.Machine(machine); err == nil {
			user = m.User
		}
	}
	return user, password
}

// SetScreenLogin saves the name and password for a machine's Screen Sharing in the keychain
// (an empty password forgets them).
func (e *Engine) SetScreenLogin(machine, user, password string) error {
	if password == "" {
		return secret.Set(secret.ScreenLoginPrefix+machine, "")
	}
	return secret.Set(secret.ScreenLoginPrefix+machine, strings.TrimSpace(user)+"\n"+password)
}
