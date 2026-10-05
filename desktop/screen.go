package main

import (
	"errors"
)

// A machine's screen in the app: see engine.ScreenCommand. The page shows it with a VNC
// viewer that talks, through the app's local server, to an ssh that reaches the machine's
// Screen Sharing.

// ScreenView is what the page needs to show a machine's screen.
type ScreenView struct {
	URL      string `json:"url"`      // the WebSocket that is the connection
	User     string `json:"user"`     // the name to sign in with
	Password string `json:"password"` // the saved password ("" when there is none yet)
}

// OpenScreen makes a connection to a machine's screen for the page.
func (a *App) OpenScreen(machine string) (ScreenView, error) {
	if a.terms == nil {
		return ScreenView{}, errors.New("the app's local server isn't running")
	}
	name, args, err := a.eng.ScreenCommand(machine)
	if err != nil {
		return ScreenView{}, err
	}
	user, password := a.eng.ScreenLogin(machine)
	return ScreenView{URL: a.terms.Pipe(name, args), User: user, Password: password}, nil
}

// SaveScreenLogin keeps the name and password for a machine's screen in the keychain.
func (a *App) SaveScreenLogin(machine, user, password string) error {
	return a.eng.SetScreenLogin(machine, user, password)
}
