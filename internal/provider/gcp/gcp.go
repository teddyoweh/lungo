// Package gcp creates machines on Google Compute Engine through gcloud.
//
// Each machine is an Ubuntu 24.04 VM plus a separate pd-balanced disk ("<name>-data") that
// holds /home. The disk is created with auto-delete off, so deleting the VM with --keep-disk
// and creating one with the same name later brings the same home directory back.
package gcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/osx"
	"skybuild/internal/provider"
)

const firewallRule = "skybuild-ssh"

// GCP implements provider.Provider.
type GCP struct{ cli provider.CLI }

func New() *GCP { return &GCP{cli: provider.CLI{Bin: "gcloud", Pre: []string{"--quiet"}}} }

func (*GCP) ID() string            { return model.ProviderGCP }
func (*GCP) Label() string         { return "Google Cloud" }
func (*GCP) DefaultRegion() string { return "us-central1" }

// Sizes: on-demand estimates for us-central1.
func (*GCP) Sizes() []model.Size {
	return []model.Size{
		{ID: "e2-standard-2", Label: "Small", CPUs: 2, MemoryGB: 8, Monthly: 48.92, Note: "One or two light sessions"},
		{ID: "e2-standard-4", Label: "Medium", CPUs: 4, MemoryGB: 16, Monthly: 97.84, Note: "A few sessions plus builds", Default: true},
		{ID: "e2-standard-8", Label: "Large", CPUs: 8, MemoryGB: 32, Monthly: 195.67, Note: "Many sessions, Docker, big repos"},
		{ID: "e2-standard-16", Label: "XL", CPUs: 16, MemoryGB: 64, Monthly: 391.34, Note: "Heavy parallel work"},
		{ID: "n2-standard-8", Label: "Fast 8", CPUs: 8, MemoryGB: 32, Monthly: 283.58, Note: "Faster CPUs for compile-heavy repos"},
		{ID: "e2-highmem-4", Label: "Memory 4", CPUs: 4, MemoryGB: 32, Monthly: 132.07, Note: "Memory-hungry tooling"},
	}
}

func (*GCP) Regions() []model.Region {
	r := func(id, label, zone string) model.Region { return model.Region{ID: id, Label: label, Zone: zone} }
	return []model.Region{
		r("us-central1", "Iowa", "us-central1-a"),
		r("us-east1", "South Carolina", "us-east1-b"),
		r("us-east4", "Virginia", "us-east4-a"),
		r("us-west1", "Oregon", "us-west1-a"),
		r("us-west2", "Los Angeles", "us-west2-a"),
		r("northamerica-northeast1", "Montréal", "northamerica-northeast1-a"),
		r("europe-west1", "Belgium", "europe-west1-b"),
		r("europe-west2", "London", "europe-west2-a"),
		r("europe-west3", "Frankfurt", "europe-west3-a"),
		r("europe-west4", "Netherlands", "europe-west4-a"),
		r("asia-northeast1", "Tokyo", "asia-northeast1-a"),
		r("asia-southeast1", "Singapore", "asia-southeast1-a"),
		r("asia-south1", "Mumbai", "asia-south1-a"),
		r("australia-southeast1", "Sydney", "australia-southeast1-a"),
		r("southamerica-east1", "São Paulo", "southamerica-east1-a"),
		r("africa-south1", "Johannesburg", "africa-south1-a"),
	}
}

func (g *GCP) Status(ctx context.Context) model.ProviderStatus {
	st := model.ProviderStatus{ID: g.ID(), Label: g.Label(), CLI: "gcloud", DiskPerGB: 0.10,
		Install: "https://cloud.google.com/sdk/docs/install  (macOS: brew install --cask google-cloud-sdk)"}
	if !osx.Has("gcloud") {
		st.Hint = "Install the Google Cloud CLI, then connect"
		return st
	}
	st.Installed = true
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var accts []struct{ Account, Status string }
	if err := g.cli.JSON(ctx, &accts, "auth", "list", "--format=json"); err == nil {
		for _, a := range accts {
			if a.Status == "ACTIVE" {
				st.Identity = a.Account
			}
		}
	}
	if st.Identity == "" {
		st.Hint = "Sign in with gcloud"
		return st
	}
	if _, err := g.cli.Run(ctx, "auth", "print-access-token"); err != nil {
		st.Hint = "gcloud sign-in expired; connect again"
		return st
	}
	st.LoggedIn = true
	def, _ := g.cli.Run(ctx, "config", "get-value", "project")
	def = strings.TrimSpace(def)
	var projects []struct{ ProjectID, Name string }
	if err := g.cli.JSON(ctx, &projects, "projects", "list", "--format=json(projectId,name)", "--limit=200", "--sort-by=projectId"); err == nil {
		for _, p := range projects {
			st.Accounts = append(st.Accounts, model.Account{ID: p.ProjectID, Label: p.Name, Default: p.ProjectID == def})
		}
	}
	if len(st.Accounts) == 0 && def != "" {
		st.Accounts = []model.Account{{ID: def, Label: def, Default: true}}
	}
	if len(st.Accounts) == 0 {
		st.Hint = "No projects found for " + st.Identity
	}
	return st
}

func (g *GCP) Login(ctx context.Context, r events.Reporter) error {
	events.Stepf(r, "Opening Google sign-in in your browser")
	_, err := g.cli.Run(ctx, "auth", "login", "--brief", "--update-adc")
	if err != nil {
		return err
	}
	events.Donef(r, "Signed in to Google Cloud")
	return nil
}

type instance struct {
	Name              string
	Status            string
	MachineType       string
	Zone              string
	Labels            map[string]string
	CreationTimestamp string
	NetworkInterfaces []struct {
		NetworkIP     string
		AccessConfigs []struct{ NatIP string }
	}
	Disks []struct {
		DeviceName string
		Source     string
		DiskSizeGb string
		Boot       bool
	}
}

func (i instance) natIP() string {
	for _, n := range i.NetworkInterfaces {
		for _, a := range n.AccessConfigs {
			if a.NatIP != "" {
				return a.NatIP
			}
		}
	}
	return ""
}

func last(s string) string { return s[strings.LastIndex(s, "/")+1:] }

func mapStatus(s string) string {
	switch s {
	case "RUNNING":
		return model.StatusRunning
	case "PROVISIONING", "STAGING":
		return model.StatusStarting
	case "STOPPING", "SUSPENDING":
		return model.StatusStopping
	case "TERMINATED", "STOPPED", "SUSPENDED":
		return model.StatusStopped
	case "REPAIRING":
		return model.StatusProvisioning
	}
	return model.StatusUnknown
}

func zoneArgs(m *model.Machine) []string {
	return []string{"--project", m.Account, "--zone", m.Zone}
}

func (g *GCP) Create(ctx context.Context, s model.Spec, r events.Reporter) (*model.Machine, error) {
	if s.Account == "" {
		return nil, errors.New("pick a Google Cloud project")
	}
	if s.Zone == "" {
		for _, reg := range g.Regions() {
			if reg.ID == s.Region {
				s.Zone = reg.Zone
			}
		}
		if s.Zone == "" {
			s.Zone = s.Region + "-a"
		}
	}
	if s.Region == "" {
		s.Region = s.Zone[:strings.LastIndex(s.Zone, "-")]
	}
	proj := []string{"--project", s.Account}

	events.Stepf(r, "Checking Compute Engine in %s", s.Account)
	if err := g.ensureFirewall(ctx, s.Account, r); err != nil {
		return nil, err
	}
	if err := g.checkQuota(ctx, s); err != nil {
		return nil, err
	}

	script, cleanup, err := provider.TempFile("sky-setup-*.sh", s.Bootstrap)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	disk := s.Name + "-data"
	diskArg := fmt.Sprintf("name=%s,size=%dGB,type=pd-balanced,device-name=skydata,auto-delete=no", disk, s.DiskGB)
	flag := "--create-disk"
	var existing struct{ SizeGb string }
	if err := g.cli.JSON(ctx, &existing, append([]string{"compute", "disks", "describe", disk, "--zone", s.Zone, "--format=json(sizeGb)"}, proj...)...); err == nil {
		events.Infof(r, "Reusing the existing %s volume (%s GB): your old /home comes back", disk, existing.SizeGb)
		flag, diskArg = "--disk", fmt.Sprintf("name=%s,device-name=skydata,auto-delete=no,mode=rw", disk)
		if n, _ := strconv.Atoi(existing.SizeGb); n > 0 {
			s.DiskGB = n
		}
	}

	image := "ubuntu-2404-lts-amd64"
	if strings.HasPrefix(s.Size, "t2a-") || strings.HasPrefix(s.Size, "c4a-") {
		image = "ubuntu-2404-lts-arm64"
	}
	events.Stepf(r, "Creating %s (%s, %s) with a %d GB volume", s.Name, s.Size, s.Zone, s.DiskGB)
	var created []instance
	args := append([]string{"compute", "instances", "create", s.Name,
		"--zone", s.Zone,
		"--machine-type", s.Size,
		"--image-family", image, "--image-project", "ubuntu-os-cloud",
		"--boot-disk-size", "30GB", "--boot-disk-type", "pd-balanced",
		flag, diskArg,
		"--metadata-from-file", "startup-script=" + script,
		"--metadata", "enable-oslogin=FALSE,block-project-ssh-keys=TRUE",
		"--tags", "skybuild",
		"--labels", fmt.Sprintf("%s=%s,%s=%s", provider.LabelKey, provider.LabelValue, provider.LabelUser, labelSafe(s.User)),
		"--format=json"}, proj...)
	err = g.cli.JSON(ctx, &created, args...)
	if err != nil && needsComputeAPI(err) {
		events.Infof(r, "Turning on the Compute Engine API for %s (first time only, ~1 min)", s.Account)
		if _, e := g.cli.Run(ctx, "services", "enable", "compute.googleapis.com", "--project", s.Account); e != nil {
			return nil, e
		}
		if err := g.ensureFirewall(ctx, s.Account, r); err != nil {
			return nil, err
		}
		err = g.cli.JSON(ctx, &created, args...)
	}
	if err != nil {
		return nil, friendly(err, s)
	}
	m := &model.Machine{
		Name: s.Name, Provider: model.ProviderGCP, Account: s.Account, Region: s.Region, Zone: s.Zone,
		Size: s.Size, DiskGB: s.DiskGB, InstanceID: s.Name, VolumeID: disk, User: s.User, OS: "linux",
		Status: model.StatusStarting,
	}
	if len(created) > 0 {
		m.PublicIP = created[0].natIP()
		m.Status = mapStatus(created[0].Status)
	}
	events.Donef(r, "VM created at %s", m.PublicIP)
	return m, nil
}

// checkQuota fails early, with a clear message, when the region can't fit the machine.
func (g *GCP) checkQuota(ctx context.Context, s model.Spec) error {
	var reg struct {
		Quotas []struct {
			Metric string
			Limit  float64
			Usage  float64
		}
	}
	if err := g.cli.JSON(ctx, &reg, "compute", "regions", "describe", s.Region, "--project", s.Account, "--format=json(quotas)"); err != nil {
		return nil // can't tell; let the create call decide
	}
	cpus := 0
	if z, ok := provider.SizeByID(g.Sizes(), s.Size); ok {
		cpus = z.CPUs
	}
	need := map[string]float64{"SSD_TOTAL_GB": float64(30 + s.DiskGB), "CPUS": float64(cpus), "IN_USE_ADDRESSES": 1}
	if strings.HasPrefix(s.Size, "e2-") {
		need["E2_CPUS"] = float64(cpus)
	}
	for _, q := range reg.Quotas {
		n, ok := need[q.Metric]
		if !ok || n == 0 || q.Usage+n <= q.Limit {
			continue
		}
		what := map[string]string{"SSD_TOTAL_GB": "SSD storage", "CPUS": "CPUs", "E2_CPUS": "E2 CPUs", "IN_USE_ADDRESSES": "public IPs"}[q.Metric]
		unit := ""
		if q.Metric == "SSD_TOTAL_GB" {
			unit = " GB"
		}
		return fmt.Errorf("not enough %s quota in %s: %.0f%s of %.0f%s used, this machine needs %.0f%s more. Pick another region (--region) or raise it at https://console.cloud.google.com/iam-admin/quotas?project=%s",
			what, s.Region, q.Usage, unit, q.Limit, unit, n, unit, s.Account)
	}
	return nil
}

// friendly trims gcloud's warnings off an error and explains quota failures in a sentence.
func friendly(err error, s model.Spec) error {
	msg := err.Error()
	if i := strings.Index(msg, "ERROR: ("); i >= 0 {
		msg = msg[i:]
	}
	if i := strings.Index(msg, "Quota '"); i >= 0 {
		q := msg[i+7:]
		if j := strings.Index(q, "'"); j > 0 {
			return fmt.Errorf("Google Cloud quota %s is used up in %s. Pick another region (--region) or raise it at https://console.cloud.google.com/iam-admin/quotas?project=%s", q[:j], s.Region, s.Account)
		}
	}
	if strings.Contains(msg, "ZONE_RESOURCE_POOL_EXHAUSTED") || strings.Contains(msg, "does not have enough resources") {
		return fmt.Errorf("%s has no %s capacity right now; try another region or size", s.Zone, s.Size)
	}
	return errors.New(strings.TrimSpace(msg))
}

func needsComputeAPI(err error) bool {
	s := err.Error()
	return strings.Contains(s, "compute.googleapis.com") && (strings.Contains(s, "not been used") || strings.Contains(s, "disabled") || strings.Contains(s, "SERVICE_DISABLED"))
}

func labelSafe(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// ensureFirewall opens SSH (and Tailscale's direct-connection port) to machines tagged skybuild.
func (g *GCP) ensureFirewall(ctx context.Context, project string, r events.Reporter) error {
	_, err := g.cli.Run(ctx, "compute", "firewall-rules", "describe", firewallRule, "--project", project, "--format=value(name)")
	if err == nil {
		return nil
	}
	if needsComputeAPI(err) {
		return nil // Create enables the API and calls us again
	}
	if !provider.IsNotFound(err) {
		return err
	}
	events.Infof(r, "Opening SSH (tcp:22) and Tailscale (udp:41641) to sky machines")
	_, err = g.cli.Run(ctx, "compute", "firewall-rules", "create", firewallRule, "--project", project,
		"--network", "default", "--direction", "INGRESS", "--allow", "tcp:22,udp:41641",
		"--target-tags", "skybuild", "--source-ranges", "0.0.0.0/0",
		"--description", "SSH and Tailscale for skybuild machines")
	if err != nil && strings.Contains(err.Error(), "networks/default") {
		return fmt.Errorf("project %s has no default VPC network; create one with `gcloud compute networks create default --project %s`", project, project)
	}
	return err
}

func (g *GCP) describe(ctx context.Context, m *model.Machine) (instance, error) {
	var i instance
	err := g.cli.JSON(ctx, &i, append([]string{"compute", "instances", "describe", m.InstanceID, "--format=json"}, zoneArgs(m)...)...)
	return i, err
}

func (g *GCP) Refresh(ctx context.Context, m *model.Machine) error {
	i, err := g.describe(ctx, m)
	if provider.IsNotFound(err) {
		m.Status = model.StatusMissing
		return nil
	}
	if err != nil {
		return err
	}
	m.Status = mapStatus(i.Status)
	m.PublicIP = i.natIP()
	m.Size = last(i.MachineType)
	return nil
}

func (g *GCP) Start(ctx context.Context, m *model.Machine) error {
	_, err := g.cli.Run(ctx, append([]string{"compute", "instances", "start", m.InstanceID}, zoneArgs(m)...)...)
	if err == nil {
		err = g.Refresh(ctx, m)
	}
	return err
}

func (g *GCP) Stop(ctx context.Context, m *model.Machine) error {
	_, err := g.cli.Run(ctx, append([]string{"compute", "instances", "stop", m.InstanceID}, zoneArgs(m)...)...)
	if err == nil {
		m.Status = model.StatusStopped
		m.PublicIP = ""
	}
	return err
}

func (g *GCP) Delete(ctx context.Context, m *model.Machine, keepDisk bool, r events.Reporter) error {
	events.Stepf(r, "Deleting VM %s", m.InstanceID)
	_, err := g.cli.Run(ctx, append([]string{"compute", "instances", "delete", m.InstanceID}, zoneArgs(m)...)...)
	if err != nil && !provider.IsNotFound(err) {
		return err
	}
	if keepDisk || m.VolumeID == "" {
		if m.VolumeID != "" {
			events.Infof(r, "Kept volume %s. `sky new %s` in %s brings it back", m.VolumeID, m.Name, m.Zone)
		}
		return nil
	}
	events.Stepf(r, "Deleting volume %s", m.VolumeID)
	_, err = g.cli.Run(ctx, append([]string{"compute", "disks", "delete", m.VolumeID}, zoneArgs(m)...)...)
	if err != nil && !provider.IsNotFound(err) {
		return err
	}
	return nil
}

func (g *GCP) Resize(ctx context.Context, m *model.Machine, size string, r events.Reporter) error {
	if err := g.Refresh(ctx, m); err != nil {
		return err
	}
	wasRunning := m.Status == model.StatusRunning || m.Status == model.StatusStarting
	if wasRunning {
		events.Stepf(r, "Stopping %s to change its size", m.Name)
		if err := g.Stop(ctx, m); err != nil {
			return err
		}
	}
	events.Stepf(r, "Changing %s to %s", m.Name, size)
	if _, err := g.cli.Run(ctx, append([]string{"compute", "instances", "set-machine-type", m.InstanceID, "--machine-type", size}, zoneArgs(m)...)...); err != nil {
		return err
	}
	m.Size = size
	if wasRunning {
		events.Stepf(r, "Starting %s", m.Name)
		return g.Start(ctx, m)
	}
	return nil
}

func (g *GCP) GrowDisk(ctx context.Context, m *model.Machine, gb int) error {
	_, err := g.cli.Run(ctx, append([]string{"compute", "disks", "resize", m.VolumeID, "--size", fmt.Sprintf("%dGB", gb)}, zoneArgs(m)...)...)
	if err == nil {
		m.DiskGB = gb
	}
	return err
}

func (g *GCP) Discover(ctx context.Context, project string) ([]*model.Machine, error) {
	var list []instance
	err := g.cli.JSON(ctx, &list, "compute", "instances", "list", "--project", project,
		"--filter", fmt.Sprintf("labels.%s=%s", provider.LabelKey, provider.LabelValue), "--format=json")
	if err != nil {
		return nil, err
	}
	var out []*model.Machine
	for _, i := range list {
		zone := last(i.Zone)
		m := &model.Machine{
			Name: i.Name, Provider: model.ProviderGCP, Account: project, Zone: zone,
			Region: zone[:strings.LastIndex(zone, "-")], Size: last(i.MachineType),
			InstanceID: i.Name, Status: mapStatus(i.Status), PublicIP: i.natIP(),
			User: i.Labels[provider.LabelUser], OS: "linux",
		}
		for _, d := range i.Disks {
			if d.DeviceName == "skydata" {
				m.VolumeID = last(d.Source)
				m.DiskGB, _ = strconv.Atoi(d.DiskSizeGb)
			}
		}
		if t, err := time.Parse(time.RFC3339, i.CreationTimestamp); err == nil {
			m.CreatedAt = t
		}
		out = append(out, m)
	}
	return out, nil
}
