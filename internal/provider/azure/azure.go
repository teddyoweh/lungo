// Package azure creates machines on Microsoft Azure through the az CLI.
//
// Each machine is an Ubuntu 24.04 VM plus a separate StandardSSD managed disk
// ("<name>-data", LUN 0) that holds /home. The disk is attached with the Detach delete
// option, so deleting the VM with --keep-disk and creating one with the same name later
// brings the same home directory back.
//
// Account is a subscription ID. Machines in a location share the resource group
// skybuild-<location>, created on demand and remembered in Machine.Extra["resourceGroup"].
package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/osx"
	"skybuild/internal/provider"
)

const (
	image   = "Canonical:ubuntu-24_04-lts:server:latest"
	diskSKU = "StandardSSD_LRS"
	volTag  = "skybuild-volume"
)

// runner runs the az binary and returns stdout. Swapped out in tests.
type runner func(ctx context.Context, args ...string) (string, error)

// Azure implements provider.Provider.
type Azure struct {
	run      runner
	has      func(string) bool
	tempFile func(pattern, content string) (string, func(), error)
}

func New() *Azure {
	// The interactive subscription picker added to `az login` would wait on stdin forever.
	cli := provider.CLI{Bin: "az", Env: []string{"AZURE_CORE_LOGIN_EXPERIENCE_V2=off", "AZURE_CORE_COLLECT_TELEMETRY=0"}}
	return &Azure{run: cli.Run, has: osx.Has, tempFile: provider.TempFile}
}

func (*Azure) ID() string            { return model.ProviderAzure }
func (*Azure) Label() string         { return "Microsoft Azure" }
func (*Azure) DefaultRegion() string { return "eastus" }

// Sizes: pay-as-you-go Linux estimates for eastus (730 hours).
func (*Azure) Sizes() []model.Size {
	return []model.Size{
		{ID: "Standard_D2s_v5", Label: "Small", CPUs: 2, MemoryGB: 8, Monthly: 70.08, Note: "One or two light sessions"},
		{ID: "Standard_D4s_v5", Label: "Medium", CPUs: 4, MemoryGB: 16, Monthly: 140.16, Note: "A few sessions plus builds", Default: true},
		{ID: "Standard_D8s_v5", Label: "Large", CPUs: 8, MemoryGB: 32, Monthly: 280.32, Note: "Many sessions, Docker, big repos"},
		{ID: "Standard_D16s_v5", Label: "XL", CPUs: 16, MemoryGB: 64, Monthly: 560.64, Note: "Heavy parallel work"},
		{ID: "Standard_B4ms", Label: "Burst 4", CPUs: 4, MemoryGB: 16, Monthly: 121.18, Note: "Burstable: cheaper when mostly idle"},
	}
}

func (*Azure) Regions() []model.Region {
	r := func(id, label string) model.Region { return model.Region{ID: id, Label: label} }
	return []model.Region{
		r("eastus", "Virginia"),
		r("eastus2", "Virginia 2"),
		r("centralus", "Iowa"),
		r("westus2", "Washington"),
		r("westus3", "Arizona"),
		r("canadacentral", "Toronto"),
		r("northeurope", "Ireland"),
		r("westeurope", "Netherlands"),
		r("uksouth", "London"),
		r("germanywestcentral", "Frankfurt"),
		r("southeastasia", "Singapore"),
		r("japaneast", "Tokyo"),
		r("australiaeast", "Sydney"),
		r("centralindia", "Pune"),
		r("brazilsouth", "São Paulo"),
	}
}

// call runs an az command in a subscription with JSON output.
func (z *Azure) call(ctx context.Context, sub string, args ...string) (string, error) {
	full := append(append([]string{}, args...), "--only-show-errors", "-o", "json")
	if sub != "" {
		full = append(full, "--subscription", sub)
	}
	return z.run(ctx, full...)
}

func (z *Azure) json(ctx context.Context, sub string, v any, args ...string) error {
	out, err := z.call(ctx, sub, args...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("az returned unexpected output: %w", err)
	}
	return nil
}

type subscription struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
	State     string `json:"state"`
	User      struct {
		Name string `json:"name"`
	} `json:"user"`
}

func (z *Azure) Status(ctx context.Context) model.ProviderStatus {
	st := model.ProviderStatus{ID: z.ID(), Label: z.Label(), CLI: "az", DiskPerGB: 0.075,
		Install: "https://learn.microsoft.com/cli/azure/install-azure-cli  (macOS: brew install azure-cli)"}
	if !z.has("az") {
		st.Hint = "Install the Azure CLI, then connect"
		return st
	}
	st.Installed = true
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var subs []subscription
	if err := z.json(ctx, "", &subs, "account", "list"); err != nil || len(subs) == 0 {
		st.Hint = "Sign in with az login"
		return st
	}
	for _, s := range subs {
		if s.State != "" && s.State != "Enabled" {
			continue
		}
		st.Accounts = append(st.Accounts, model.Account{ID: s.ID, Label: s.Name, Default: s.IsDefault})
		if s.IsDefault || st.Identity == "" {
			st.Identity = s.User.Name
		}
	}
	if len(st.Accounts) == 0 {
		st.Hint = "No enabled subscriptions for " + subs[0].User.Name
		return st
	}
	st.LoggedIn = true
	return st
}

func (z *Azure) Login(ctx context.Context, r events.Reporter) error {
	events.Stepf(r, "Opening Microsoft sign-in in your browser")
	if _, err := z.run(ctx, "login", "--only-show-errors", "-o", "none"); err != nil {
		return err
	}
	events.Donef(r, "Signed in to Azure")
	return nil
}

func rgFor(location string) string { return "skybuild-" + location }

func rgOf(m *model.Machine) string {
	if rg := m.Extra["resourceGroup"]; rg != "" {
		return rg
	}
	return rgFor(m.Region)
}

func mapPower(s string) string {
	switch s {
	case "VM running":
		return model.StatusRunning
	case "VM starting":
		return model.StatusStarting
	case "VM stopping", "VM deallocating":
		return model.StatusStopping
	case "VM stopped", "VM deallocated":
		return model.StatusStopped
	}
	return model.StatusUnknown
}

func firstIP(list string) string {
	ip, _, _ := strings.Cut(list, ",")
	return strings.TrimSpace(ip)
}

type disk struct {
	Name       string            `json:"name"`
	DiskSizeGB int               `json:"diskSizeGb"`
	DiskState  string            `json:"diskState"`
	Tags       map[string]string `json:"tags"`
}

func (z *Azure) Create(ctx context.Context, s model.Spec, r events.Reporter) (*model.Machine, error) {
	sub := s.Account
	if sub == "" {
		return nil, errors.New("pick an Azure subscription")
	}
	loc := s.Region
	if loc == "" {
		loc = z.DefaultRegion()
	}
	rg := rgFor(loc)
	events.Stepf(r, "Checking Azure in %s", loc)
	if _, err := z.call(ctx, sub, "group", "create", "--name", rg, "--location", loc,
		"--tags", provider.LabelKey+"="+provider.LabelValue); err != nil {
		return nil, err
	}

	diskName := s.Name + "-data"
	var d disk
	err := z.json(ctx, sub, &d, "disk", "show", "--resource-group", rg, "--name", diskName)
	switch {
	case err == nil:
		if d.DiskState != "" && d.DiskState != "Unattached" {
			return nil, fmt.Errorf("volume %s is %s to another VM; delete that machine first", diskName, strings.ToLower(d.DiskState))
		}
		s.DiskGB = d.DiskSizeGB
		events.Infof(r, "Reusing the existing %s volume (%d GB): your old /home comes back", diskName, d.DiskSizeGB)
	case provider.IsNotFound(err):
		events.Infof(r, "Creating a %d GB volume", s.DiskGB)
		if _, err := z.call(ctx, sub, "disk", "create", "--resource-group", rg, "--name", diskName,
			"--location", loc, "--size-gb", fmt.Sprint(s.DiskGB), "--sku", diskSKU,
			"--tags", provider.LabelKey+"="+provider.LabelValue, provider.LabelUser+"="+s.User, volTag+"="+s.Name); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}

	// Azure creates the admin user (with its home on the OS disk) before custom-data runs;
	// the setup script then copies /home onto the data disk before mounting it there.
	script, cleanScript, err := z.tempFile("sky-setup-*.sh", s.Bootstrap)
	if err != nil {
		return nil, err
	}
	defer cleanScript()
	key, cleanKey, err := z.tempFile("sky-key-*.pub", strings.TrimSpace(s.PublicKey)+"\n")
	if err != nil {
		return nil, err
	}
	defer cleanKey()

	events.Stepf(r, "Creating %s (%s, %s) with a %d GB volume", s.Name, s.Size, loc, s.DiskGB)
	var vm struct {
		PublicIPAddress string `json:"publicIpAddress"`
		PowerState      string `json:"powerState"`
	}
	if err := z.json(ctx, sub, &vm, "vm", "create",
		"--resource-group", rg, "--name", s.Name, "--location", loc,
		"--image", image, "--size", s.Size,
		"--admin-username", s.User, "--ssh-key-values", key,
		"--custom-data", script,
		"--attach-data-disks", diskName,
		"--os-disk-size-gb", "30", "--storage-sku", diskSKU,
		"--public-ip-sku", "Standard", "--nsg-rule", "SSH",
		"--os-disk-delete-option", "Delete", "--nic-delete-option", "Delete", "--data-disk-delete-option", "Detach",
		"--tags", provider.LabelKey+"="+provider.LabelValue, provider.LabelUser+"="+s.User); err != nil {
		return nil, err
	}
	if _, err := z.call(ctx, sub, "network", "nsg", "rule", "create", "--resource-group", rg,
		"--nsg-name", s.Name+"NSG", "--name", "tailscale", "--priority", "1010",
		"--direction", "Inbound", "--access", "Allow", "--protocol", "Udp",
		"--destination-port-ranges", "41641", "--source-address-prefixes", "*"); err != nil {
		events.Warnf(r, "Could not open udp:41641 for Tailscale direct connections (it still works through relays): %v", err)
	}
	m := &model.Machine{
		Name: s.Name, Provider: model.ProviderAzure, Account: sub, Region: loc, Size: s.Size,
		DiskGB: s.DiskGB, InstanceID: s.Name, VolumeID: diskName, User: s.User, OS: "linux",
		Status: model.StatusStarting, PublicIP: vm.PublicIPAddress,
		Extra: map[string]string{"resourceGroup": rg},
	}
	if st := mapPower(vm.PowerState); st != model.StatusUnknown {
		m.Status = st
	}
	events.Donef(r, "VM created at %s", m.PublicIP)
	return m, nil
}

type vmDetails struct {
	Name            string            `json:"name"`
	Location        string            `json:"location"`
	ResourceGroup   string            `json:"resourceGroup"`
	PowerState      string            `json:"powerState"`
	PublicIps       string            `json:"publicIps"`
	Tags            map[string]string `json:"tags"`
	TimeCreated     string            `json:"timeCreated"`
	HardwareProfile struct {
		VMSize string `json:"vmSize"`
	} `json:"hardwareProfile"`
	StorageProfile struct {
		DataDisks []struct {
			Name       string `json:"name"`
			Lun        int    `json:"lun"`
			DiskSizeGB int    `json:"diskSizeGb"`
		} `json:"dataDisks"`
	} `json:"storageProfile"`
}

func (z *Azure) show(ctx context.Context, m *model.Machine) (vmDetails, error) {
	var v vmDetails
	err := z.json(ctx, m.Account, &v, "vm", "show", "--show-details", "--resource-group", rgOf(m), "--name", m.InstanceID)
	return v, err
}

func (z *Azure) Refresh(ctx context.Context, m *model.Machine) error {
	v, err := z.show(ctx, m)
	if provider.IsNotFound(err) {
		m.Status = model.StatusMissing
		return nil
	}
	if err != nil {
		return err
	}
	m.Status = mapPower(v.PowerState)
	m.PublicIP = firstIP(v.PublicIps)
	if v.HardwareProfile.VMSize != "" {
		m.Size = v.HardwareProfile.VMSize
	}
	return nil
}

func (z *Azure) vmOp(ctx context.Context, m *model.Machine, op string) error {
	_, err := z.call(ctx, m.Account, "vm", op, "--resource-group", rgOf(m), "--name", m.InstanceID)
	return err
}

func (z *Azure) Start(ctx context.Context, m *model.Machine) error {
	if err := z.vmOp(ctx, m, "start"); err != nil {
		return err
	}
	return z.Refresh(ctx, m)
}

// Stop deallocates, so compute billing stops (a plain stop keeps charging). The Standard
// public IP is static, so the address survives.
func (z *Azure) Stop(ctx context.Context, m *model.Machine) error {
	if err := z.vmOp(ctx, m, "deallocate"); err != nil {
		return err
	}
	m.Status = model.StatusStopped
	return nil
}

func (z *Azure) Delete(ctx context.Context, m *model.Machine, keepDisk bool, r events.Reporter) error {
	rg := rgOf(m)
	events.Stepf(r, "Deleting VM %s", m.InstanceID)
	_, err := z.call(ctx, m.Account, "vm", "delete", "--resource-group", rg, "--name", m.InstanceID, "--yes")
	if err != nil && !provider.IsNotFound(err) {
		return err
	}
	// The NIC and OS disk go with the VM; the public IP, NSG and VNet az vm create made don't.
	for _, res := range [][]string{
		{"network", "public-ip", "delete", "--resource-group", rg, "--name", m.InstanceID + "PublicIP"},
		{"network", "nsg", "delete", "--resource-group", rg, "--name", m.InstanceID + "NSG"},
		{"network", "vnet", "delete", "--resource-group", rg, "--name", m.InstanceID + "VNET"},
	} {
		_, _ = z.call(ctx, m.Account, res...) // best effort: missing or shared is fine
	}
	if keepDisk || m.VolumeID == "" {
		if m.VolumeID != "" {
			events.Infof(r, "Kept volume %s. `sky new %s` in %s brings it back", m.VolumeID, m.Name, m.Region)
		}
		return nil
	}
	events.Stepf(r, "Deleting volume %s", m.VolumeID)
	_, err = z.call(ctx, m.Account, "disk", "delete", "--resource-group", rg, "--name", m.VolumeID, "--yes")
	if err != nil && !provider.IsNotFound(err) {
		return err
	}
	return nil
}

func sizeUnavailable(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not available") || strings.Contains(s, "allocationfailed") || strings.Contains(s, "cluster")
}

func (z *Azure) Resize(ctx context.Context, m *model.Machine, size string, r events.Reporter) error {
	if err := z.Refresh(ctx, m); err != nil {
		return err
	}
	wasRunning := m.Status == model.StatusRunning || m.Status == model.StatusStarting
	events.Stepf(r, "Changing %s to %s", m.Name, size)
	resize := func() error {
		_, err := z.call(ctx, m.Account, "vm", "resize", "--resource-group", rgOf(m), "--name", m.InstanceID, "--size", size)
		return err
	}
	err := resize()
	if err != nil && wasRunning && sizeUnavailable(err) {
		events.Infof(r, "%s isn't available on the current host; stopping %s to move it", size, m.Name)
		if err := z.Stop(ctx, m); err != nil {
			return err
		}
		if err := resize(); err != nil {
			return err
		}
		m.Size = size
		events.Stepf(r, "Starting %s", m.Name)
		return z.Start(ctx, m)
	}
	if err != nil {
		return err
	}
	m.Size = size
	return z.Refresh(ctx, m)
}

// GrowDisk deallocates a running VM first: managed disks attached to a running VM can't
// always be resized in place.
func (z *Azure) GrowDisk(ctx context.Context, m *model.Machine, gb int) error {
	if err := z.Refresh(ctx, m); err != nil {
		return err
	}
	wasRunning := m.Status == model.StatusRunning || m.Status == model.StatusStarting
	if wasRunning {
		if err := z.Stop(ctx, m); err != nil {
			return err
		}
	}
	if _, err := z.call(ctx, m.Account, "disk", "update", "--resource-group", rgOf(m), "--name", m.VolumeID, "--size-gb", fmt.Sprint(gb)); err != nil {
		return err
	}
	m.DiskGB = gb
	if wasRunning {
		return z.Start(ctx, m)
	}
	return nil
}

func (z *Azure) Discover(ctx context.Context, sub string) ([]*model.Machine, error) {
	var list []vmDetails
	if err := z.json(ctx, sub, &list, "vm", "list", "--show-details",
		"--query", fmt.Sprintf("[?tags.%s=='%s']", provider.LabelKey, provider.LabelValue)); err != nil {
		return nil, err
	}
	var out []*model.Machine
	for _, v := range list {
		m := &model.Machine{
			Name: v.Name, Provider: model.ProviderAzure, Account: sub, Region: v.Location,
			Size: v.HardwareProfile.VMSize, InstanceID: v.Name, Status: mapPower(v.PowerState),
			PublicIP: firstIP(v.PublicIps), User: v.Tags[provider.LabelUser], OS: "linux",
			Extra: map[string]string{"resourceGroup": v.ResourceGroup},
		}
		for _, d := range v.StorageProfile.DataDisks {
			if d.Lun == 0 {
				m.VolumeID, m.DiskGB = d.Name, d.DiskSizeGB
			}
		}
		if t, err := time.Parse(time.RFC3339, v.TimeCreated); err == nil {
			m.CreatedAt = t
		}
		out = append(out, m)
	}
	return out, nil
}
