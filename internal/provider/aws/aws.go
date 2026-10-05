// Package aws creates machines on Amazon EC2 through the AWS CLI (v2).
//
// Each machine is an Ubuntu 24.04 instance plus a separate gp3 volume (/dev/sdf) that holds
// /home. The volume is created with DeleteOnTermination off and tagged skybuild-volume=<name>,
// so deleting the machine with --keep-disk and creating one with the same name later brings
// the same home directory back.
//
// Account is an AWS CLI profile. Every call passes --profile and --region explicitly, so the
// user's own default region and AWS_PROFILE never change where sky puts things.
package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/osx"
	"skybuild/internal/provider"
)

const (
	securityGroup = "skybuild"
	volumeTag     = "skybuild-volume" // = machine name, only on the data volume
	rootDevice    = "/dev/sda1"       // Canonical's Ubuntu AMIs boot from sda1
	dataDevice    = "/dev/sdf"
	amiAMD64      = "resolve:ssm:/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
	amiARM64      = "resolve:ssm:/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id"
	stsRegion     = "us-east-1" // any commercial region answers sts get-caller-identity
)

// runner runs the aws binary and returns stdout. Swapped out in tests.
type runner func(ctx context.Context, args ...string) (string, error)

// AWS implements provider.Provider.
type AWS struct {
	run      runner
	has      func(string) bool
	tempFile func(pattern, content string) (string, func(), error)
	getenv   func(string) string
}

func New() *AWS {
	cli := provider.CLI{Bin: "aws", Env: []string{"AWS_PAGER="}}
	return &AWS{run: cli.Run, has: osx.Has, tempFile: provider.TempFile, getenv: os.Getenv}
}

func (*AWS) ID() string            { return model.ProviderAWS }
func (*AWS) Label() string         { return "Amazon Web Services" }
func (*AWS) DefaultRegion() string { return "us-east-1" }

// Sizes: on-demand Linux estimates for us-east-1 (730 hours).
func (*AWS) Sizes() []model.Size {
	return []model.Size{
		{ID: "t3.large", Label: "Small", CPUs: 2, MemoryGB: 8, Monthly: 60.74, Note: "One or two light sessions (burstable)"},
		{ID: "t3.xlarge", Label: "Medium", CPUs: 4, MemoryGB: 16, Monthly: 121.47, Note: "A few sessions plus builds (burstable)", Default: true},
		{ID: "m7i.xlarge", Label: "Steady 4", CPUs: 4, MemoryGB: 16, Monthly: 147.17, Note: "Full-time CPU, no burst credits"},
		{ID: "m7i.2xlarge", Label: "Large", CPUs: 8, MemoryGB: 32, Monthly: 294.34, Note: "Many sessions, Docker, big repos"},
		{ID: "m7i.4xlarge", Label: "XL", CPUs: 16, MemoryGB: 64, Monthly: 588.67, Note: "Heavy parallel work"},
		{ID: "m7g.xlarge", Label: "Graviton 4", CPUs: 4, MemoryGB: 16, Monthly: 119.14, Note: "ARM (arm64): cheaper, but some tools ship x86 only"},
	}
}

func (*AWS) Regions() []model.Region {
	r := func(id, label string) model.Region { return model.Region{ID: id, Label: label} }
	return []model.Region{
		r("us-east-1", "N. Virginia"),
		r("us-east-2", "Ohio"),
		r("us-west-2", "Oregon"),
		r("ca-central-1", "Canada"),
		r("eu-west-1", "Ireland"),
		r("eu-west-2", "London"),
		r("eu-central-1", "Frankfurt"),
		r("ap-southeast-1", "Singapore"),
		r("ap-northeast-1", "Tokyo"),
		r("ap-south-1", "Mumbai"),
		r("ap-southeast-2", "Sydney"),
		r("sa-east-1", "São Paulo"),
	}
}

// call runs an aws command for a profile and region with JSON output.
func (a *AWS) call(ctx context.Context, profile, region string, args ...string) (string, error) {
	full := append(append([]string{}, args...), "--output", "json")
	if region != "" {
		full = append(full, "--region", region)
	}
	if p := a.profileArg(profile); p != "" {
		full = append(full, "--profile", p)
	}
	return a.run(ctx, full...)
}

// profileArg leaves --profile off for "default" so environment credentials keep working,
// unless AWS_PROFILE points somewhere else and "default" has to be named.
func (a *AWS) profileArg(profile string) string {
	if profile == "" {
		return ""
	}
	if profile == "default" {
		if env := a.getenv("AWS_PROFILE"); env == "" || env == "default" {
			return ""
		}
	}
	return profile
}

func (a *AWS) json(ctx context.Context, profile, region string, v any, args ...string) error {
	out, err := a.call(ctx, profile, region, args...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("aws returned unexpected output: %w", err)
	}
	return nil
}

func (a *AWS) profiles(ctx context.Context) []string {
	out, _ := a.run(ctx, "configure", "list-profiles")
	var list []string
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			list = append(list, l)
		}
	}
	if !seen["default"] && a.getenv("AWS_ACCESS_KEY_ID") != "" {
		list = append([]string{"default"}, list...)
	}
	return list
}

func (a *AWS) Status(ctx context.Context) model.ProviderStatus {
	st := model.ProviderStatus{ID: a.ID(), Label: a.Label(), CLI: "aws", DiskPerGB: 0.08,
		Install: "https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html  (macOS: brew install awscli)"}
	if !a.has("aws") {
		st.Hint = "Install the AWS CLI, then connect"
		return st
	}
	st.Installed = true
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	profiles := a.profiles(ctx)
	if len(profiles) == 0 {
		st.Hint = "Sign in with `aws login` (or add keys with `aws configure`)"
		return st
	}
	type who struct {
		Account, Arn string
		ok           bool
	}
	ids := make([]who, len(profiles))
	var wg sync.WaitGroup
	for i, p := range profiles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var w struct{ Account, Arn string }
			if err := a.json(ctx, p, stsRegion, &w, "sts", "get-caller-identity"); err == nil && w.Arn != "" {
				ids[i] = who{w.Account, w.Arn, true}
			}
		}()
	}
	wg.Wait()
	def := a.getenv("AWS_PROFILE")
	if def == "" {
		def = "default"
	}
	hasDefault := false
	for i, p := range profiles {
		if !ids[i].ok {
			continue
		}
		isDef := p == def
		hasDefault = hasDefault || isDef
		st.Accounts = append(st.Accounts, model.Account{ID: p, Label: fmt.Sprintf("%s (%s)", p, ids[i].Account), Default: isDef})
		if isDef || st.Identity == "" {
			st.Identity = ids[i].Arn
		}
	}
	if len(st.Accounts) == 0 {
		st.Hint = "AWS credentials are missing or expired; connect again"
		return st
	}
	if !hasDefault {
		st.Accounts[0].Default = true
	}
	st.LoggedIn = true
	return st
}

// Login prefers `aws login` (browser sign-in with console credentials), then SSO, and
// otherwise explains how to add access keys.
func (a *AWS) Login(ctx context.Context, r events.Reporter) error {
	if _, err := a.run(ctx, "login", "help"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "invalid choice") {
		events.Stepf(r, "Opening AWS sign-in in your browser")
		if _, err := a.run(ctx, "login"); err != nil {
			return err
		}
		events.Donef(r, "Signed in to AWS")
		return nil
	}
	for _, p := range a.profiles(ctx) {
		url, _ := a.run(ctx, "configure", "get", "sso_start_url", "--profile", p)
		sess, _ := a.run(ctx, "configure", "get", "sso_session", "--profile", p)
		if strings.TrimSpace(url) == "" && strings.TrimSpace(sess) == "" {
			continue
		}
		events.Stepf(r, "Opening AWS SSO sign-in for %s in your browser", p)
		if _, err := a.run(ctx, "sso", "login", "--profile", p); err != nil {
			return err
		}
		events.Donef(r, "Signed in to AWS (%s)", p)
		return nil
	}
	return errors.New("this AWS CLI has no browser sign-in; run `aws configure` in a terminal to add access keys (or update the AWS CLI), then connect again")
}

type tag struct{ Key, Value string }

type instance struct {
	InstanceId          string
	InstanceType        string
	PublicIpAddress     string
	LaunchTime          string
	State               struct{ Name string }
	Placement           struct{ AvailabilityZone string }
	Tags                []tag
	BlockDeviceMappings []struct {
		DeviceName string
		Ebs        struct{ VolumeId string }
	}
}

func (i instance) tag(k string) string {
	for _, t := range i.Tags {
		if t.Key == k {
			return t.Value
		}
	}
	return ""
}

func (i instance) volume(device string) string {
	for _, b := range i.BlockDeviceMappings {
		if b.DeviceName == device {
			return b.Ebs.VolumeId
		}
	}
	return ""
}

type reservations struct {
	Reservations []struct{ Instances []instance }
}

func (r reservations) all() []instance {
	var out []instance
	for _, res := range r.Reservations {
		out = append(out, res.Instances...)
	}
	return out
}

type volume struct {
	VolumeId         string
	Size             int
	State            string
	AvailabilityZone string
}

func mapStatus(s string) string {
	switch s {
	case "running":
		return model.StatusRunning
	case "pending":
		return model.StatusStarting
	case "stopping", "shutting-down":
		return model.StatusStopping
	case "stopped":
		return model.StatusStopped
	case "terminated":
		return model.StatusMissing
	}
	return model.StatusUnknown
}

var family = regexp.MustCompile(`^([a-z]+)(\d+)([a-z-]*)\.`)

// isGraviton reports whether an instance type is ARM (m7g, c7gn, t4g, g5g…).
func isGraviton(size string) bool {
	m := family.FindStringSubmatch(size)
	return m != nil && strings.Contains(m[3], "g")
}

// KeyPairName is the EC2 key pair for a public key: stable per key, so one import per region.
func KeyPairName(pub string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(pub)))
	return "skybuild-" + hex.EncodeToString(sum[:])[:8]
}

func tagSpec(kind string, tags ...tag) string {
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = fmt.Sprintf("{Key=%s,Value=%s}", t.Key, t.Value)
	}
	return fmt.Sprintf("ResourceType=%s,Tags=[%s]", kind, strings.Join(parts, ","))
}

func (a *AWS) ensureKeyPair(ctx context.Context, profile, region, pub string, r events.Reporter) (string, error) {
	name := KeyPairName(pub)
	_, err := a.call(ctx, profile, region, "ec2", "describe-key-pairs", "--key-names", name)
	if err == nil {
		return name, nil
	}
	if !provider.IsNotFound(err) {
		return "", err
	}
	events.Infof(r, "Importing sky's SSH key as %s", name)
	file, cleanup, err := a.tempFile("sky-key-*.pub", strings.TrimSpace(pub)+"\n")
	if err != nil {
		return "", err
	}
	defer cleanup()
	_, err = a.call(ctx, profile, region, "ec2", "import-key-pair", "--key-name", name,
		"--public-key-material", "fileb://"+file,
		"--tag-specifications", tagSpec("key-pair", tag{provider.LabelKey, provider.LabelValue}))
	return name, err
}

func (a *AWS) defaultVPC(ctx context.Context, profile, region string) (string, error) {
	var out struct{ Vpcs []struct{ VpcId string } }
	if err := a.json(ctx, profile, region, &out, "ec2", "describe-vpcs", "--filters", "Name=is-default,Values=true"); err != nil {
		return "", err
	}
	if len(out.Vpcs) == 0 {
		return "", fmt.Errorf("%s has no default VPC; create one with `aws ec2 create-default-vpc --region %s`", region, region)
	}
	return out.Vpcs[0].VpcId, nil
}

// ensureSecurityGroup opens SSH (and Tailscale's direct-connection port) to sky machines.
func (a *AWS) ensureSecurityGroup(ctx context.Context, profile, region, vpc string, r events.Reporter) (string, error) {
	var found struct{ SecurityGroups []struct{ GroupId string } }
	if err := a.json(ctx, profile, region, &found, "ec2", "describe-security-groups",
		"--filters", "Name=group-name,Values="+securityGroup, "Name=vpc-id,Values="+vpc); err != nil {
		return "", err
	}
	if len(found.SecurityGroups) > 0 {
		return found.SecurityGroups[0].GroupId, nil
	}
	events.Infof(r, "Opening SSH (tcp:22) and Tailscale (udp:41641) to sky machines")
	var created struct{ GroupId string }
	if err := a.json(ctx, profile, region, &created, "ec2", "create-security-group",
		"--group-name", securityGroup, "--description", "SSH and Tailscale for skybuild machines", "--vpc-id", vpc,
		"--tag-specifications", tagSpec("security-group", tag{provider.LabelKey, provider.LabelValue})); err != nil {
		return "", err
	}
	_, err := a.call(ctx, profile, region, "ec2", "authorize-security-group-ingress", "--group-id", created.GroupId,
		"--ip-permissions",
		"IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=0.0.0.0/0}]",
		"IpProtocol=udp,FromPort=41641,ToPort=41641,IpRanges=[{CidrIp=0.0.0.0/0}]")
	return created.GroupId, err
}

// keptVolume finds a volume left behind by `sky rm --keep-disk` for this name.
func (a *AWS) keptVolume(ctx context.Context, profile, region, name string) (*volume, error) {
	var out struct{ Volumes []volume }
	if err := a.json(ctx, profile, region, &out, "ec2", "describe-volumes",
		"--filters", "Name=tag:"+volumeTag+",Values="+name, "Name=status,Values=available"); err != nil {
		return nil, err
	}
	if len(out.Volumes) == 0 {
		return nil, nil
	}
	return &out.Volumes[0], nil
}

type ebs struct {
	VolumeSize          int    `json:"VolumeSize"`
	VolumeType          string `json:"VolumeType"`
	DeleteOnTermination bool   `json:"DeleteOnTermination"`
}

type mapping struct {
	DeviceName string `json:"DeviceName"`
	Ebs        ebs    `json:"Ebs"`
}

func (a *AWS) Create(ctx context.Context, s model.Spec, r events.Reporter) (*model.Machine, error) {
	profile := s.Account
	if profile == "" {
		return nil, errors.New("pick an AWS profile")
	}
	region := s.Region
	if region == "" {
		region = a.DefaultRegion()
	}
	events.Stepf(r, "Checking AWS in %s (%s)", region, profile)
	kp, err := a.ensureKeyPair(ctx, profile, region, s.PublicKey, r)
	if err != nil {
		return nil, err
	}
	vpc, err := a.defaultVPC(ctx, profile, region)
	if err != nil {
		return nil, err
	}
	sg, err := a.ensureSecurityGroup(ctx, profile, region, vpc, r)
	if err != nil {
		return nil, err
	}
	kept, err := a.keptVolume(ctx, profile, region, s.Name)
	if err != nil {
		return nil, err
	}

	bdm := []mapping{{DeviceName: rootDevice, Ebs: ebs{VolumeSize: 30, VolumeType: "gp3", DeleteOnTermination: true}}}
	if kept != nil {
		// The setup script finds the data disk by size ("auto"), so it must match the volume.
		s.DiskGB = kept.Size
		events.Infof(r, "Reusing the existing %s volume (%d GB): your old /home comes back", kept.VolumeId, kept.Size)
	} else {
		bdm = append(bdm, mapping{DeviceName: dataDevice, Ebs: ebs{VolumeSize: s.DiskGB, VolumeType: "gp3", DeleteOnTermination: false}})
	}
	bdmJSON, _ := json.Marshal(bdm)

	script, cleanup, err := a.tempFile("sky-setup-*.sh", s.Bootstrap)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	ami := amiAMD64
	if isGraviton(s.Size) {
		ami = amiARM64
	}
	labels := []tag{{provider.LabelKey, provider.LabelValue}, {provider.LabelUser, s.User}}
	args := []string{"ec2", "run-instances",
		"--image-id", ami,
		"--instance-type", s.Size,
		"--key-name", kp,
		"--security-group-ids", sg,
		"--block-device-mappings", string(bdmJSON),
		"--user-data", "file://" + script,
		"--metadata-options", "HttpTokens=required,HttpEndpoint=enabled",
		"--tag-specifications",
		tagSpec("instance", append([]tag{{"Name", s.Name}}, labels...)...),
		tagSpec("volume", labels...),
		"--count", "1"}
	if kept != nil {
		args = append(args, "--placement", "AvailabilityZone="+kept.AvailabilityZone)
	}
	events.Stepf(r, "Creating %s (%s, %s) with a %d GB volume", s.Name, s.Size, region, s.DiskGB)
	var launched struct{ Instances []instance }
	if err := a.json(ctx, profile, region, &launched, args...); err != nil {
		return nil, err
	}
	if len(launched.Instances) == 0 {
		return nil, errors.New("aws run-instances returned no instance")
	}
	id := launched.Instances[0].InstanceId

	events.Infof(r, "Waiting for %s to boot", id)
	if _, err := a.call(ctx, profile, region, "ec2", "wait", "instance-running", "--instance-ids", id); err != nil {
		return nil, err
	}
	if kept != nil {
		if _, err := a.call(ctx, profile, region, "ec2", "attach-volume", "--volume-id", kept.VolumeId,
			"--instance-id", id, "--device", dataDevice); err != nil {
			return nil, fmt.Errorf("attaching %s: %w", kept.VolumeId, err)
		}
	}
	inst, err := a.describe(ctx, profile, region, id)
	if err != nil {
		return nil, err
	}
	m := &model.Machine{
		Name: s.Name, Provider: model.ProviderAWS, Account: profile, Region: region,
		Zone: inst.Placement.AvailabilityZone, Size: s.Size, DiskGB: s.DiskGB, InstanceID: id,
		User: s.User, OS: "linux", Status: mapStatus(inst.State.Name), PublicIP: inst.PublicIpAddress,
		Extra: map[string]string{"keyPair": kp, "securityGroup": sg, "vpc": vpc},
	}
	if kept != nil {
		m.VolumeID = kept.VolumeId
	} else {
		m.VolumeID = inst.volume(dataDevice)
		if m.VolumeID != "" {
			if _, err := a.call(ctx, profile, region, "ec2", "create-tags", "--resources", m.VolumeID,
				"--tags", "Key="+volumeTag+",Value="+s.Name, "Key=Name,Value="+s.Name+"-data"); err != nil {
				events.Warnf(r, "Could not tag volume %s: %v", m.VolumeID, err)
			}
		}
	}
	events.Donef(r, "VM created at %s", m.PublicIP)
	return m, nil
}

func (a *AWS) describe(ctx context.Context, profile, region, id string) (instance, error) {
	var out reservations
	if err := a.json(ctx, profile, region, &out, "ec2", "describe-instances", "--instance-ids", id); err != nil {
		return instance{}, err
	}
	all := out.all()
	if len(all) == 0 {
		return instance{}, fmt.Errorf("instance %s was not found", id)
	}
	return all[0], nil
}

func (a *AWS) Refresh(ctx context.Context, m *model.Machine) error {
	inst, err := a.describe(ctx, m.Account, m.Region, m.InstanceID)
	if provider.IsNotFound(err) {
		m.Status = model.StatusMissing
		return nil
	}
	if err != nil {
		return err
	}
	m.Status = mapStatus(inst.State.Name)
	m.PublicIP = inst.PublicIpAddress
	if inst.InstanceType != "" {
		m.Size = inst.InstanceType
	}
	return nil
}

func (a *AWS) Start(ctx context.Context, m *model.Machine) error {
	if _, err := a.call(ctx, m.Account, m.Region, "ec2", "start-instances", "--instance-ids", m.InstanceID); err != nil {
		return err
	}
	if _, err := a.call(ctx, m.Account, m.Region, "ec2", "wait", "instance-running", "--instance-ids", m.InstanceID); err != nil {
		return err
	}
	return a.Refresh(ctx, m)
}

func (a *AWS) Stop(ctx context.Context, m *model.Machine) error {
	if _, err := a.call(ctx, m.Account, m.Region, "ec2", "stop-instances", "--instance-ids", m.InstanceID); err != nil {
		return err
	}
	if _, err := a.call(ctx, m.Account, m.Region, "ec2", "wait", "instance-stopped", "--instance-ids", m.InstanceID); err != nil {
		return err
	}
	m.Status = model.StatusStopped
	m.PublicIP = "" // a stopped instance gives its public IP back
	return nil
}

func (a *AWS) Delete(ctx context.Context, m *model.Machine, keepDisk bool, r events.Reporter) error {
	events.Stepf(r, "Deleting instance %s", m.InstanceID)
	_, err := a.call(ctx, m.Account, m.Region, "ec2", "terminate-instances", "--instance-ids", m.InstanceID)
	if err != nil && !provider.IsNotFound(err) {
		return err
	}
	if err == nil {
		if _, err := a.call(ctx, m.Account, m.Region, "ec2", "wait", "instance-terminated", "--instance-ids", m.InstanceID); err != nil {
			return err
		}
	}
	if keepDisk || m.VolumeID == "" {
		if m.VolumeID != "" {
			events.Infof(r, "Kept volume %s. `sky new %s` in %s brings it back", m.VolumeID, m.Name, m.Region)
		}
		return nil
	}
	events.Stepf(r, "Deleting volume %s", m.VolumeID)
	if _, err := a.call(ctx, m.Account, m.Region, "ec2", "wait", "volume-available", "--volume-ids", m.VolumeID); err != nil && !provider.IsNotFound(err) {
		return err
	}
	_, err = a.call(ctx, m.Account, m.Region, "ec2", "delete-volume", "--volume-id", m.VolumeID)
	if err != nil && !provider.IsNotFound(err) {
		return err
	}
	return nil
}

func (a *AWS) Resize(ctx context.Context, m *model.Machine, size string, r events.Reporter) error {
	if err := a.Refresh(ctx, m); err != nil {
		return err
	}
	wasRunning := m.Status == model.StatusRunning || m.Status == model.StatusStarting
	if wasRunning {
		events.Stepf(r, "Stopping %s to change its size", m.Name)
		if err := a.Stop(ctx, m); err != nil {
			return err
		}
	}
	events.Stepf(r, "Changing %s to %s", m.Name, size)
	if _, err := a.call(ctx, m.Account, m.Region, "ec2", "modify-instance-attribute", "--instance-id", m.InstanceID,
		"--instance-type", fmt.Sprintf(`{"Value": %q}`, size)); err != nil {
		return err
	}
	m.Size = size
	if wasRunning {
		events.Stepf(r, "Starting %s", m.Name)
		return a.Start(ctx, m)
	}
	return nil
}

func (a *AWS) GrowDisk(ctx context.Context, m *model.Machine, gb int) error {
	_, err := a.call(ctx, m.Account, m.Region, "ec2", "modify-volume", "--volume-id", m.VolumeID, "--size", fmt.Sprint(gb))
	if err == nil {
		m.DiskGB = gb
	}
	return err
}

// Discover looks in every listed region, since a profile isn't tied to one.
func (a *AWS) Discover(ctx context.Context, profile string) ([]*model.Machine, error) {
	regions := a.Regions()
	found := make([][]*model.Machine, len(regions))
	errs := make([]error, len(regions))
	var wg sync.WaitGroup
	for i, reg := range regions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			found[i], errs[i] = a.discoverRegion(ctx, profile, reg.ID)
		}()
	}
	wg.Wait()
	var out []*model.Machine
	failed := 0
	for i := range regions {
		if errs[i] != nil {
			failed++
			continue
		}
		out = append(out, found[i]...)
	}
	if failed == len(regions) {
		return nil, errs[0]
	}
	return out, nil
}

func (a *AWS) discoverRegion(ctx context.Context, profile, region string) ([]*model.Machine, error) {
	var res reservations
	if err := a.json(ctx, profile, region, &res, "ec2", "describe-instances", "--filters",
		"Name=tag:"+provider.LabelKey+",Values="+provider.LabelValue,
		"Name=instance-state-name,Values=pending,running,stopping,stopped"); err != nil {
		return nil, err
	}
	insts := res.all()
	if len(insts) == 0 {
		return nil, nil
	}
	sizes := map[string]int{}
	var vols []string
	for _, i := range insts {
		if v := i.volume(dataDevice); v != "" {
			vols = append(vols, v)
		}
	}
	if len(vols) > 0 {
		var vs struct{ Volumes []volume }
		if err := a.json(ctx, profile, region, &vs, append([]string{"ec2", "describe-volumes", "--volume-ids"}, vols...)...); err == nil {
			for _, v := range vs.Volumes {
				sizes[v.VolumeId] = v.Size
			}
		}
	}
	var out []*model.Machine
	for _, i := range insts {
		name := i.tag("Name")
		if name == "" {
			name = i.InstanceId
		}
		m := &model.Machine{
			Name: name, Provider: model.ProviderAWS, Account: profile, Region: region,
			Zone: i.Placement.AvailabilityZone, Size: i.InstanceType, InstanceID: i.InstanceId,
			Status: mapStatus(i.State.Name), PublicIP: i.PublicIpAddress, User: i.tag(provider.LabelUser), OS: "linux",
			VolumeID: i.volume(dataDevice),
		}
		m.DiskGB = sizes[m.VolumeID]
		if t, err := time.Parse(time.RFC3339, i.LaunchTime); err == nil {
			m.CreatedAt = t
		}
		out = append(out, m)
	}
	return out, nil
}
