// sky runs your Claude Code sessions on cloud machines: create a VM with a persistent
// volume, `ssh <name>` into it, keep your logins and tools in sync, move folders, open ports.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"skybuild/internal/bootstrap"
	"skybuild/internal/engine"
	"skybuild/internal/events"
)

var version = "dev"

var (
	eng      = engine.New()
	jsonOut  bool
	verbose  bool
	noBrowse bool
)

func main() {
	cobra.EnableCommandSorting = false
	bootstrap.Version = version
	root := &cobra.Command{
		Use:           "sky",
		Short:         "Cloud machines for your Claude Code sessions",
		Long:          "sky creates cloud machines with persistent volumes, keeps your Claude Code, GitHub and CLI logins in sync on them, and puts every machine one `ssh <name>` away.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return overview(cmd.Context()) },
		Version:       version,
	}
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "print JSON (for scripts)")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "show every line of output from machines")
	root.PersistentFlags().BoolVar(&noBrowse, "no-browser", false, "print links instead of opening them")

	root.AddGroup(
		&cobra.Group{ID: "machines", Title: "Machines"},
		&cobra.Group{ID: "work", Title: "Working on a machine"},
		&cobra.Group{ID: "sync", Title: "Sync and files"},
		&cobra.Group{ID: "setup", Title: "Accounts and setup"},
	)
	for _, c := range machineCommands() {
		c.GroupID = "machines"
		root.AddCommand(c)
	}
	for _, c := range workCommands() {
		c.GroupID = "work"
		root.AddCommand(c)
	}
	for _, c := range syncCommands() {
		c.GroupID = "sync"
		root.AddCommand(c)
	}
	for _, c := range setupCommands() {
		c.GroupID = "setup"
		root.AddCommand(c)
	}
	for _, c := range historyCommands() {
		c.GroupID = "work"
		root.AddCommand(c)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, sDim.Render("cancelled"))
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, sRed.Render("✗ ")+err.Error())
		os.Exit(1)
	}
}

// progress runs fn with a live progress renderer.
func progress(fn func(r events.Reporter) error) error {
	if jsonOut {
		return fn(events.Discard)
	}
	r := newRenderer(verbose)
	r.noBrowse = noBrowse
	err := fn(r)
	r.end(err != nil)
	return err
}
