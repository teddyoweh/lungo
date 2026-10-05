// Package model holds the types shared by the CLI, the desktop app and every provider.
package model

import "time"

// Machine statuses. Providers map their own states onto these.
const (
	StatusRunning      = "running"
	StatusStopped      = "stopped"
	StatusStarting     = "starting"
	StatusStopping     = "stopping"
	StatusProvisioning = "provisioning"
	StatusUnreachable  = "unreachable"
	StatusUnknown      = "unknown"
	StatusMissing      = "missing" // the cloud no longer has it
)

// Provider IDs.
const (
	ProviderGCP   = "gcp"
	ProviderAWS   = "aws"
	ProviderAzure = "azure"
	ProviderSSH   = "ssh" // a machine you already have: just host, user and key
)

// Machine is one box sky manages. Cloud machines carry an instance and a data volume;
// SSH machines only carry connection details.
type Machine struct {
	Name     string `json:"name"`              // also the SSH alias: `ssh <name>`
	Provider string `json:"provider"`          // gcp | aws | azure | ssh
	Account  string `json:"account,omitempty"` // GCP project, AWS profile or Azure subscription
	Region   string `json:"region,omitempty"`
	Zone     string `json:"zone,omitempty"`
	Size     string `json:"size,omitempty"`
	DiskGB   int    `json:"diskGB,omitempty"` // persistent data volume mounted at /home

	InstanceID string `json:"instanceId,omitempty"`
	VolumeID   string `json:"volumeId,omitempty"`

	Status        string `json:"status,omitempty"`
	PublicIP      string `json:"publicIp,omitempty"`
	TailscaleIP   string `json:"tailscaleIp,omitempty"`
	TailscaleName string `json:"tailscaleName,omitempty"`

	Host     string `json:"host,omitempty"` // SSH machines: hostname or IP
	Port     int    `json:"port,omitempty"`
	User     string `json:"user"`
	KeyPath  string `json:"keyPath,omitempty"`  // empty = sky's own key
	SSHAlias string `json:"sshAlias,omitempty"` // reuse an alias from ~/.ssh/config instead of managing one
	OS       string `json:"os,omitempty"`       // linux | darwin, learned on first connect

	Sync []string `json:"sync"` // sync item IDs enabled for this machine

	Extra     map[string]string `json:"extra,omitempty"` // provider bookkeeping (security group, resource group…)
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// IsCloud reports whether sky created the machine and can start, stop and resize it.
func (m *Machine) IsCloud() bool { return m.Provider != ProviderSSH }

// Address is the host sky connects to: Tailscale first, then the public IP, then Host.
func (m *Machine) Address(preferTailscale bool) string {
	if preferTailscale && m.TailscaleIP != "" {
		return m.TailscaleIP
	}
	if m.PublicIP != "" {
		return m.PublicIP
	}
	if m.Host != "" {
		return m.Host
	}
	return m.TailscaleIP
}

// SSHPort defaults to 22.
func (m *Machine) SSHPort() int {
	if m.Port == 0 {
		return 22
	}
	return m.Port
}

// Size is a machine type offered in the picker.
type Size struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"` // "Medium"
	CPUs     int     `json:"cpus"`
	MemoryGB float64 `json:"memoryGB"`
	Monthly  float64 `json:"monthly"` // estimated on-demand USD per month in the default region
	Note     string  `json:"note,omitempty"`
	Default  bool    `json:"default,omitempty"`
}

// Region is a place to put a machine. Zone is set for providers that pick a zone (GCP).
type Region struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Zone  string `json:"zone,omitempty"`
}

// Account is a GCP project, AWS profile or Azure subscription the user can create machines in.
type Account struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Default bool   `json:"default,omitempty"`
}

// ProviderStatus says whether a provider is ready to use and what is missing if not.
type ProviderStatus struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CLI       string    `json:"cli"`
	Installed bool      `json:"installed"`
	LoggedIn  bool      `json:"loggedIn"`
	Identity  string    `json:"identity,omitempty"` // signed-in user or profile
	Accounts  []Account `json:"accounts,omitempty"`
	Hint      string    `json:"hint,omitempty"`    // next step when not ready
	Install   string    `json:"install,omitempty"` // how to install the CLI
	DiskPerGB float64   `json:"diskPerGB"`         // estimated USD per GB-month for the data volume
}

// Spec is everything needed to create a cloud machine.
type Spec struct {
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Account   string `json:"account"`
	Region    string `json:"region"`
	Zone      string `json:"zone,omitempty"`
	Size      string `json:"size"`
	DiskGB    int    `json:"diskGB"`
	User      string `json:"user"`
	Tailscale bool   `json:"tailscale"`
	Docker    bool   `json:"docker"`
	// Filled in by the engine before the provider sees it.
	PublicKey string `json:"-"`
	Bootstrap string `json:"-"` // rendered first-boot script
}

// Port is a TCP port something is listening on inside a machine.
type Port struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
	Process string `json:"process,omitempty"`
}

// Link pairs a local folder with a folder on a machine.
type Link struct {
	ID        string    `json:"id"`
	Local     string    `json:"local"`
	Machine   string    `json:"machine"`
	Remote    string    `json:"remote"`
	Direction string    `json:"direction"` // push | pull
	Excludes  []string  `json:"excludes,omitempty"`
	Delete    bool      `json:"delete,omitempty"`
	Watch     bool      `json:"watch,omitempty"`
	LastSync  time.Time `json:"lastSync,omitempty"`
}

// ClaudeAccount is one Claude subscription machines can sign in with. The token itself lives
// in the OS keychain under its ID; this is the non-secret part.
type ClaudeAccount struct {
	ID           string         `json:"id"`              // accountUuid when known, else a random ID
	Label        string         `json:"label"`           // what the user calls it ("Work Max")
	Email        string         `json:"email,omitempty"` // known for the account this computer is signed in to
	Disabled     bool           `json:"disabled,omitempty"`
	AddedAt      time.Time      `json:"addedAt"`
	OAuthAccount map[string]any `json:"oauthAccount,omitempty"` // display details for machines' ~/.claude.json
}

// Name is the label, falling back to the email.
func (a *ClaudeAccount) Name() string {
	if a.Label != "" {
		return a.Label
	}
	if a.Email != "" {
		return a.Email
	}
	return a.ID
}

// APIKey is an API key sky stores and puts on machines as an environment variable. The value
// lives in the OS keychain; this is the non-secret part.
type APIKey struct {
	Name     string    `json:"name"`               // OPENAI_API_KEY
	Provider string    `json:"provider,omitempty"` // catalogue ID ("openai"), "" for custom
	Machines []string  `json:"machines,omitempty"` // empty = every machine
	AddedAt  time.Time `json:"addedAt"`
}

// On reports whether the key goes to the named machine.
func (k *APIKey) On(machine string) bool {
	if len(k.Machines) == 0 {
		return true
	}
	for _, m := range k.Machines {
		if m == machine {
			return true
		}
	}
	return false
}
