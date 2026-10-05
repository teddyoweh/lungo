package term

// Probe is what a terminal is doing right now.
type Probe struct {
	Cwd     string `json:"cwd"`     // working directory of the program in front
	Command string `json:"command"` // its name: zsh, claude, vim…
	Claude  bool   `json:"claude"`  // Claude Code is running in this terminal
}
