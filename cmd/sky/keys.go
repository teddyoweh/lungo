package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"skybuild/internal/apikeys"
	"skybuild/internal/events"
)

func keysCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "keys",
		Short: "API keys (OpenAI, Anthropic, Gemini…) kept in your keychain and put on machines",
		Long: `API keys sky puts on your machines as environment variables (in ~/.secrets.sh, loaded by
every shell). Keys stored with ` + "`sky keys add`" + ` live in your keychain; keys exported from your shell
startup files are picked up too. Changes go to machines right away.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			views, err := eng.APIKeys(cmd.Context())
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(views)
			}
			if len(views) == 0 {
				fmt.Println(hint("No API keys yet. Add one: sky keys add OPENAI_API_KEY"))
				return nil
			}
			var rows [][]string
			for _, v := range views {
				from := sAccent.Render("sky")
				if !v.Stored {
					from = sDim.Render(v.Shell)
				} else if v.Shell != "" {
					from += sDim.Render(" (overrides " + v.Shell + ")")
				}
				where := "all machines"
				if len(v.Machines) > 0 {
					where = strings.Join(v.Machines, ", ")
				}
				if !v.Sync {
					where = sDim.Render("not synced")
				}
				label := v.Label
				if label == v.Name {
					label = ""
				}
				rows = append(rows, []string{sBold.Render(v.Name), label, sDim.Render(v.Masked), from, where})
			}
			table(os.Stdout, []string{"key", "service", "value", "from", "goes to"}, rows)
			return nil
		},
	}

	var machines string
	var noPush bool
	add := &cobra.Command{
		Use:   "add <NAME|service> [value]",
		Short: "Store a key (prompts for the value) and put it on machines",
		Example: `  sky keys add OPENAI_API_KEY            # paste when asked
  sky keys add openai                    # same: the service picks the variable name
  sky keys add GEMINI_API_KEY --machines box,lab
  pbpaste | sky keys add ANTHROPIC_API_KEY -`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, provider := args[0], ""
			if p, ok := apikeys.ByID(strings.ToLower(name)); ok {
				name, provider = p.Env, p.ID
			}
			name = strings.ToUpper(name)
			if err := apikeys.ValidName(name); err != nil {
				return err
			}
			value := ""
			if len(args) == 2 && args[1] != "-" {
				value = args[1]
			} else if len(args) == 2 || !term.IsTerminal(int(os.Stdin.Fd())) {
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				value = strings.TrimSpace(line)
			} else {
				fmt.Fprintf(os.Stderr, "Paste %s (hidden): ", name)
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return err
				}
				value = strings.TrimSpace(string(b))
			}
			var ms []string
			if machines != "" && machines != "all" {
				ms = strings.Split(machines, ",")
			}
			if err := eng.SetAPIKey(name, value, provider, ms); err != nil {
				return err
			}
			fmt.Printf("%s %s saved in your keychain %s\n", sGreen.Render("✓"), sBold.Render(name), sDim.Render(apikeys.Mask(value)))
			return pushKeys(cmd, noPush)
		},
	}
	add.Flags().StringVar(&machines, "machines", "", "only these machines (comma-separated; default all)")
	add.Flags().BoolVar(&noPush, "no-push", false, "don't sync to machines now")

	rm := &cobra.Command{
		Use:   "rm <NAME>",
		Short: "Delete a stored key (it comes off machines too)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := eng.RemoveAPIKey(strings.ToUpper(args[0])); err != nil {
				return err
			}
			fmt.Println(sGreen.Render("✓ ") + "removed " + args[0])
			return pushKeys(cmd, noPush)
		},
	}
	syncOnOff := &cobra.Command{
		Use:   "sync <NAME> <on|off>",
		Short: "Choose whether a key goes to machines (works for shell-file keys too)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] != "on" && args[1] != "off" {
				return errors.New("on or off")
			}
			if err := eng.SetAPIKeySync(strings.ToUpper(args[0]), args[1] == "on"); err != nil {
				return err
			}
			return pushKeys(cmd, noPush)
		},
	}
	where := &cobra.Command{
		Use:   "machines <NAME> <machine,machine|all>",
		Short: "Choose which machines get a stored key",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var ms []string
			if args[1] != "all" {
				ms = strings.Split(args[1], ",")
			}
			if err := eng.SetAPIKeyMachines(strings.ToUpper(args[0]), ms); err != nil {
				return err
			}
			return pushKeys(cmd, noPush)
		},
	}
	test := &cobra.Command{
		Use:   "test [NAME]",
		Short: "Check keys against their services (all testable keys by default)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			views, err := eng.APIKeys(cmd.Context())
			if err != nil {
				return err
			}
			for _, v := range views {
				if len(args) == 1 && !strings.EqualFold(v.Name, args[0]) || len(args) == 0 && !v.CanTest {
					continue
				}
				ok, detail, err := eng.TestAPIKey(cmd.Context(), v.Name)
				switch {
				case err != nil:
					fmt.Printf("  %s %-28s %s\n", sDim.Render("○"), v.Name, sDim.Render(err.Error()))
				case ok:
					fmt.Printf("  %s %-28s %s\n", sGreen.Render("●"), v.Name, detail)
				default:
					fmt.Printf("  %s %-28s %s\n", sRed.Render("●"), v.Name, detail)
				}
			}
			return nil
		},
	}
	providers := &cobra.Command{
		Use:   "services",
		Short: "Services sky knows (their variable names and where to make a key)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var rows [][]string
			for _, p := range apikeys.Providers {
				t := ""
				if p.CanTest() {
					t = sGreen.Render("testable")
				}
				rows = append(rows, []string{p.ID, p.Label, sBold.Render(p.Env), t, sDim.Render(p.Docs)})
			}
			table(os.Stdout, []string{"id", "service", "variable", "", "make one at"}, rows)
			return nil
		},
	}
	for _, sc := range []*cobra.Command{rm, syncOnOff, where} {
		sc.Flags().BoolVar(&noPush, "no-push", false, "don't sync to machines now")
	}
	c.AddCommand(add, rm, syncOnOff, where, test, providers)
	return c
}

func pushKeys(cmd *cobra.Command, skip bool) error {
	if skip {
		return nil
	}
	ms, _ := eng.Machines()
	if len(ms) == 0 {
		return nil
	}
	return progress(func(r events.Reporter) error {
		events.Stepf(r, "Updating keys on machines")
		res := eng.PushKeys(cmd.Context(), r)
		n := 0
		for _, x := range res {
			if x.Skipped == "" && len(x.Errors) == 0 {
				n++
			}
		}
		events.Donef(r, "Keys up to date on %d machine(s)", n)
		return nil
	})
}
