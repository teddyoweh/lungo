//go:build !darwin

package main

import "errors"

// Elsewhere Lungo doesn't replace itself (selfUpdatable says so); updates come from the
// releases page.

func canReplace(string) bool { return false }

func (a *App) startInstaller(bool) error {
	return errors.New("updating itself isn't supported on this system")
}

func installMain([]string) {}
