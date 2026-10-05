package aws

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"skybuild/internal/events"
	"skybuild/internal/model"
)

// fake records every aws invocation and answers from a function.
type fake struct {
	mu      sync.Mutex
	calls   [][]string
	respond func(args []string) (string, error)
}

func (f *fake) run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{}, args...))
	f.mu.Unlock()
	if f.respond == nil {
		return "", nil
	}
	return f.respond(args)
}

func is(args []string, prefix ...string) bool {
	if len(args) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if args[i] != p {
			return false
		}
	}
	return true
}

func newTest(f *fake, env map[string]string) *AWS {
	return &AWS{
		run: f.run,
		has: func(string) bool { return true },
		tempFile: func(pattern, _ string) (string, func(), error) {
			if strings.Contains(pattern, "key") {
				return "/tmp/sky-key.pub", func() {}, nil
			}
			return "/tmp/sky-setup.sh", func() {}, nil
		},
		getenv: func(k string) string { return env[k] },
	}
}

func j(args ...string) []string {
	return append(args, "--output", "json", "--region", "us-east-1", "--profile", "work")
}

var notFound = errors.New("An error occurred (InvalidKeyPair.NotFound) when calling the DescribeKeyPairs operation: The key pair 'x' does not exist")

const runningInstance = `{"Reservations":[{"Instances":[{
  "InstanceId":"i-0abc","InstanceType":"t3.xlarge","PublicIpAddress":"3.91.10.20",
  "LaunchTime":"2026-10-03T12:00:00+00:00","State":{"Code":16,"Name":"running"},
  "Placement":{"AvailabilityZone":"us-east-1a"},
  "Tags":[{"Key":"Name","Value":"api-box"},{"Key":"skybuild","Value":"machine"},{"Key":"sky-user","Value":"teddy"}],
  "BlockDeviceMappings":[
    {"DeviceName":"/dev/sda1","Ebs":{"VolumeId":"vol-root","Status":"attached"}},
    {"DeviceName":"/dev/sdf","Ebs":{"VolumeId":"vol-data","Status":"attached"}}]}]}]}`

func spec() model.Spec {
	return model.Spec{Name: "api-box", Provider: "aws", Account: "work", Region: "us-east-1", Size: "t3.xlarge",
		DiskGB: 100, User: "teddy", PublicKey: "ssh-ed25519 AAAAC3Nza skybuild@mac", Bootstrap: "#!/bin/bash"}
}

func TestCreateNew(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		switch {
		case is(a, "ec2", "describe-key-pairs"):
			return "", notFound
		case is(a, "ec2", "describe-vpcs"):
			return `{"Vpcs":[{"VpcId":"vpc-1","IsDefault":true}]}`, nil
		case is(a, "ec2", "describe-security-groups"):
			return `{"SecurityGroups":[]}`, nil
		case is(a, "ec2", "create-security-group"):
			return `{"GroupId":"sg-1"}`, nil
		case is(a, "ec2", "describe-volumes"):
			return `{"Volumes":[]}`, nil
		case is(a, "ec2", "run-instances"):
			return `{"Instances":[{"InstanceId":"i-0abc","State":{"Name":"pending"},"Placement":{"AvailabilityZone":"us-east-1a"}}]}`, nil
		case is(a, "ec2", "describe-instances"):
			return runningInstance, nil
		}
		return "", nil
	}}
	a := newTest(f, nil)
	m, err := a.Create(context.Background(), spec(), events.Discard)
	if err != nil {
		t.Fatal(err)
	}
	kp := KeyPairName(spec().PublicKey)
	want := [][]string{
		j("ec2", "describe-key-pairs", "--key-names", kp),
		j("ec2", "import-key-pair", "--key-name", kp, "--public-key-material", "fileb:///tmp/sky-key.pub",
			"--tag-specifications", "ResourceType=key-pair,Tags=[{Key=skybuild,Value=machine}]"),
		j("ec2", "describe-vpcs", "--filters", "Name=is-default,Values=true"),
		j("ec2", "describe-security-groups", "--filters", "Name=group-name,Values=skybuild", "Name=vpc-id,Values=vpc-1"),
		j("ec2", "create-security-group", "--group-name", "skybuild", "--description", "SSH and Tailscale for skybuild machines",
			"--vpc-id", "vpc-1", "--tag-specifications", "ResourceType=security-group,Tags=[{Key=skybuild,Value=machine}]"),
		j("ec2", "authorize-security-group-ingress", "--group-id", "sg-1", "--ip-permissions",
			"IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=0.0.0.0/0}]",
			"IpProtocol=udp,FromPort=41641,ToPort=41641,IpRanges=[{CidrIp=0.0.0.0/0}]"),
		j("ec2", "describe-volumes", "--filters", "Name=tag:skybuild-volume,Values=api-box", "Name=status,Values=available"),
		j("ec2", "run-instances",
			"--image-id", amiAMD64,
			"--instance-type", "t3.xlarge",
			"--key-name", kp,
			"--security-group-ids", "sg-1",
			"--block-device-mappings", `[{"DeviceName":"/dev/sda1","Ebs":{"VolumeSize":30,"VolumeType":"gp3","DeleteOnTermination":true}},{"DeviceName":"/dev/sdf","Ebs":{"VolumeSize":100,"VolumeType":"gp3","DeleteOnTermination":false}}]`,
			"--user-data", "file:///tmp/sky-setup.sh",
			"--metadata-options", "HttpTokens=required,HttpEndpoint=enabled",
			"--tag-specifications",
			"ResourceType=instance,Tags=[{Key=Name,Value=api-box},{Key=skybuild,Value=machine},{Key=sky-user,Value=teddy}]",
			"ResourceType=volume,Tags=[{Key=skybuild,Value=machine},{Key=sky-user,Value=teddy}]",
			"--count", "1"),
		j("ec2", "wait", "instance-running", "--instance-ids", "i-0abc"),
		j("ec2", "describe-instances", "--instance-ids", "i-0abc"),
		j("ec2", "create-tags", "--resources", "vol-data", "--tags", "Key=skybuild-volume,Value=api-box", "Key=Name,Value=api-box-data"),
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", dump(f.calls), dump(want))
	}
	if m.InstanceID != "i-0abc" || m.VolumeID != "vol-data" || m.PublicIP != "3.91.10.20" || m.Zone != "us-east-1a" ||
		m.Status != model.StatusRunning || m.DiskGB != 100 || m.Extra["securityGroup"] != "sg-1" || m.Extra["keyPair"] != kp {
		t.Fatalf("machine: %+v", m)
	}
}

func TestCreateReusesKeptVolume(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		switch {
		case is(a, "ec2", "describe-vpcs"):
			return `{"Vpcs":[{"VpcId":"vpc-1"}]}`, nil
		case is(a, "ec2", "describe-security-groups"):
			return `{"SecurityGroups":[{"GroupId":"sg-9"}]}`, nil
		case is(a, "ec2", "describe-volumes"):
			return `{"Volumes":[{"VolumeId":"vol-old","Size":250,"State":"available","AvailabilityZone":"us-east-1c"}]}`, nil
		case is(a, "ec2", "run-instances"):
			return `{"Instances":[{"InstanceId":"i-0abc"}]}`, nil
		case is(a, "ec2", "describe-instances"):
			return runningInstance, nil
		}
		return "", nil
	}}
	a := newTest(f, nil)
	rec := &events.Recorder{}
	s := spec()
	s.Size = "m7g.xlarge"
	m, err := a.Create(context.Background(), s, rec)
	if err != nil {
		t.Fatal(err)
	}
	var run, attach []string
	for _, c := range f.calls {
		if is(c, "ec2", "run-instances") {
			run = c
		}
		if is(c, "ec2", "attach-volume") {
			attach = c
		}
		if is(c, "ec2", "import-key-pair") || is(c, "ec2", "create-security-group") || is(c, "ec2", "create-tags") {
			t.Fatalf("unexpected call %v", c)
		}
	}
	joined := strings.Join(run, " ")
	if !strings.Contains(joined, amiARM64) || strings.Contains(joined, "/dev/sdf") ||
		!strings.Contains(joined, "--placement AvailabilityZone=us-east-1c") {
		t.Fatalf("run-instances: %v", run)
	}
	if !reflect.DeepEqual(attach, j("ec2", "attach-volume", "--volume-id", "vol-old", "--instance-id", "i-0abc", "--device", "/dev/sdf")) {
		t.Fatalf("attach: %v", attach)
	}
	if m.VolumeID != "vol-old" || m.DiskGB != 250 {
		t.Fatalf("machine: %+v", m)
	}
	reused := false
	for _, e := range rec.Events {
		reused = reused || strings.Contains(e.Message, "Reusing the existing vol-old")
	}
	if !reused {
		t.Fatal("no reuse message")
	}
}

func TestNoDefaultVPC(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		if is(a, "ec2", "describe-vpcs") {
			return `{"Vpcs":[]}`, nil
		}
		return "", nil
	}}
	_, err := newTest(f, nil).Create(context.Background(), spec(), events.Discard)
	if err == nil || !strings.Contains(err.Error(), "create-default-vpc") {
		t.Fatalf("err = %v", err)
	}
}

func machine() *model.Machine {
	return &model.Machine{Name: "api-box", Provider: "aws", Account: "work", Region: "us-east-1", Zone: "us-east-1a",
		InstanceID: "i-0abc", VolumeID: "vol-data", Size: "t3.xlarge", DiskGB: 100}
}

func TestOperations(t *testing.T) {
	stopped := strings.Replace(runningInstance, `"Name":"running"`, `"Name":"stopped"`, 1)
	cases := []struct {
		name  string
		state string // what describe-instances reports
		do    func(a *AWS, m *model.Machine) error
		want  [][]string
	}{
		{"start", runningInstance, func(a *AWS, m *model.Machine) error { return a.Start(context.Background(), m) }, [][]string{
			j("ec2", "start-instances", "--instance-ids", "i-0abc"),
			j("ec2", "wait", "instance-running", "--instance-ids", "i-0abc"),
			j("ec2", "describe-instances", "--instance-ids", "i-0abc"),
		}},
		{"stop", runningInstance, func(a *AWS, m *model.Machine) error { return a.Stop(context.Background(), m) }, [][]string{
			j("ec2", "stop-instances", "--instance-ids", "i-0abc"),
			j("ec2", "wait", "instance-stopped", "--instance-ids", "i-0abc"),
		}},
		{"delete", runningInstance, func(a *AWS, m *model.Machine) error { return a.Delete(context.Background(), m, false, events.Discard) }, [][]string{
			j("ec2", "terminate-instances", "--instance-ids", "i-0abc"),
			j("ec2", "wait", "instance-terminated", "--instance-ids", "i-0abc"),
			j("ec2", "wait", "volume-available", "--volume-ids", "vol-data"),
			j("ec2", "delete-volume", "--volume-id", "vol-data"),
		}},
		{"delete keep disk", runningInstance, func(a *AWS, m *model.Machine) error { return a.Delete(context.Background(), m, true, events.Discard) }, [][]string{
			j("ec2", "terminate-instances", "--instance-ids", "i-0abc"),
			j("ec2", "wait", "instance-terminated", "--instance-ids", "i-0abc"),
		}},
		{"resize running", runningInstance, func(a *AWS, m *model.Machine) error {
			return a.Resize(context.Background(), m, "m7i.2xlarge", events.Discard)
		}, [][]string{
			j("ec2", "describe-instances", "--instance-ids", "i-0abc"),
			j("ec2", "stop-instances", "--instance-ids", "i-0abc"),
			j("ec2", "wait", "instance-stopped", "--instance-ids", "i-0abc"),
			j("ec2", "modify-instance-attribute", "--instance-id", "i-0abc", "--instance-type", `{"Value": "m7i.2xlarge"}`),
			j("ec2", "start-instances", "--instance-ids", "i-0abc"),
			j("ec2", "wait", "instance-running", "--instance-ids", "i-0abc"),
			j("ec2", "describe-instances", "--instance-ids", "i-0abc"),
		}},
		{"resize stopped", stopped, func(a *AWS, m *model.Machine) error {
			return a.Resize(context.Background(), m, "m7i.2xlarge", events.Discard)
		}, [][]string{
			j("ec2", "describe-instances", "--instance-ids", "i-0abc"),
			j("ec2", "modify-instance-attribute", "--instance-id", "i-0abc", "--instance-type", `{"Value": "m7i.2xlarge"}`),
		}},
		{"grow disk", runningInstance, func(a *AWS, m *model.Machine) error { return a.GrowDisk(context.Background(), m, 300) }, [][]string{
			j("ec2", "modify-volume", "--volume-id", "vol-data", "--size", "300"),
		}},
		{"refresh", runningInstance, func(a *AWS, m *model.Machine) error { return a.Refresh(context.Background(), m) }, [][]string{
			j("ec2", "describe-instances", "--instance-ids", "i-0abc"),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{respond: func(a []string) (string, error) {
				if is(a, "ec2", "describe-instances") {
					return c.state, nil
				}
				return "", nil
			}}
			m := machine()
			if err := c.do(newTest(f, nil), m); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.calls, c.want) {
				t.Fatalf("calls:\n%s\nwant:\n%s", dump(f.calls), dump(c.want))
			}
		})
	}
}

func TestStopClearsIPAndGrowRecordsSize(t *testing.T) {
	a := newTest(&fake{}, nil)
	m := machine()
	m.PublicIP = "1.2.3.4"
	if err := a.Stop(context.Background(), m); err != nil || m.Status != model.StatusStopped || m.PublicIP != "" {
		t.Fatalf("stop: %v %+v", err, m)
	}
	if err := a.GrowDisk(context.Background(), m, 300); err != nil || m.DiskGB != 300 {
		t.Fatalf("grow: %v %+v", err, m)
	}
}

func TestRefreshMissing(t *testing.T) {
	f := &fake{respond: func([]string) (string, error) {
		return "", errors.New("An error occurred (InvalidInstanceID.NotFound) when calling the DescribeInstances operation: The instance ID 'i-0abc' does not exist")
	}}
	m := machine()
	if err := newTest(f, nil).Refresh(context.Background(), m); err != nil || m.Status != model.StatusMissing {
		t.Fatalf("%v %s", err, m.Status)
	}
}

func TestDeleteAlreadyGone(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		if is(a, "ec2", "terminate-instances") {
			return "", errors.New("An error occurred (InvalidInstanceID.NotFound)")
		}
		return "", nil
	}}
	if err := newTest(f, nil).Delete(context.Background(), machine(), false, events.Discard); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if is(c, "ec2", "wait", "instance-terminated") {
			t.Fatal("waited on a missing instance")
		}
	}
}

func TestMapStatus(t *testing.T) {
	for in, want := range map[string]string{
		"pending": model.StatusStarting, "running": model.StatusRunning, "stopping": model.StatusStopping,
		"shutting-down": model.StatusStopping, "stopped": model.StatusStopped, "terminated": model.StatusMissing,
		"weird": model.StatusUnknown,
	} {
		if got := mapStatus(in); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}

func TestGraviton(t *testing.T) {
	for size, want := range map[string]bool{
		"m7g.xlarge": true, "c7gn.large": true, "t4g.small": true, "m6gd.large": true, "g5g.xlarge": true,
		"t3.large": false, "m7i.xlarge": false, "g5.xlarge": false, "m7a.large": false, "r7iz.large": false,
	} {
		if got := isGraviton(size); got != want {
			t.Errorf("%s: %v, want %v", size, got, want)
		}
	}
}

func TestDiscover(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		region := a[len(a)-3] // … --region <r> --profile work
		switch {
		case is(a, "ec2", "describe-instances") && region == "us-east-1":
			return runningInstance, nil
		case is(a, "ec2", "describe-instances") && region == "eu-west-1":
			return "", errors.New("An error occurred (UnauthorizedOperation)")
		case is(a, "ec2", "describe-instances"):
			return `{"Reservations":[]}`, nil
		case is(a, "ec2", "describe-volumes"):
			return `{"Volumes":[{"VolumeId":"vol-data","Size":120}]}`, nil
		}
		return "", nil
	}}
	got, err := newTest(f, nil).Discover(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d machines", len(got))
	}
	m := got[0]
	if m.Name != "api-box" || m.User != "teddy" || m.VolumeID != "vol-data" || m.DiskGB != 120 || m.Region != "us-east-1" ||
		m.Zone != "us-east-1a" || m.Status != model.StatusRunning || m.CreatedAt.IsZero() {
		t.Fatalf("machine: %+v", m)
	}
	var filter []string
	for _, c := range f.calls {
		if is(c, "ec2", "describe-instances") && c[len(c)-3] == "us-east-1" {
			filter = c
		}
	}
	want := j("ec2", "describe-instances", "--filters", "Name=tag:skybuild,Values=machine",
		"Name=instance-state-name,Values=pending,running,stopping,stopped")
	if !reflect.DeepEqual(filter, want) {
		t.Fatalf("filter: %v", filter)
	}
}

func TestStatus(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		switch {
		case is(a, "configure", "list-profiles"):
			return "default\nwork\nold\n", nil
		case is(a, "sts", "get-caller-identity"):
			if strings.Contains(strings.Join(a, " "), "--profile old") {
				return "", errors.New("ExpiredToken")
			}
			if strings.Contains(strings.Join(a, " "), "--profile work") {
				return `{"UserId":"AIDA2","Account":"222222222222","Arn":"arn:aws:iam::222222222222:user/work"}`, nil
			}
			return `{"UserId":"AIDA1","Account":"111111111111","Arn":"arn:aws:iam::111111111111:user/teddy"}`, nil
		}
		return "", nil
	}}
	st := newTest(f, nil).Status(context.Background())
	if !st.Installed || !st.LoggedIn || st.Identity != "arn:aws:iam::111111111111:user/teddy" || len(st.Accounts) != 2 {
		t.Fatalf("%+v", st)
	}
	if st.Accounts[0] != (model.Account{ID: "default", Label: "default (111111111111)", Default: true}) ||
		st.Accounts[1].Label != "work (222222222222)" {
		t.Fatalf("%+v", st.Accounts)
	}
	for _, c := range f.calls {
		if is(c, "sts") && strings.Contains(strings.Join(c, " "), "--profile default") {
			t.Fatal("default profile should not be passed explicitly")
		}
	}
}

func TestStatusEnvCredentialsAndNotInstalled(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		if is(a, "sts") {
			return `{"Account":"333","Arn":"arn:aws:iam::333:user/ci"}`, nil
		}
		return "", nil
	}}
	st := newTest(f, map[string]string{"AWS_ACCESS_KEY_ID": "AKIA"}).Status(context.Background())
	if !st.LoggedIn || len(st.Accounts) != 1 || st.Accounts[0].ID != "default" {
		t.Fatalf("%+v", st)
	}
	a := newTest(&fake{}, nil)
	a.has = func(string) bool { return false }
	if st := a.Status(context.Background()); st.Installed || st.Hint == "" {
		t.Fatalf("%+v", st)
	}
}

func TestProfileArg(t *testing.T) {
	a := newTest(&fake{}, map[string]string{"AWS_PROFILE": "work"})
	if a.profileArg("default") != "default" || a.profileArg("work") != "work" || a.profileArg("") != "" {
		t.Fatal("profileArg with AWS_PROFILE set")
	}
	if newTest(&fake{}, nil).profileArg("default") != "" {
		t.Fatal("default should be implicit")
	}
}

func TestLogin(t *testing.T) {
	f := &fake{}
	if err := newTest(f, nil).Login(context.Background(), events.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, [][]string{{"login", "help"}, {"login"}}) {
		t.Fatalf("%v", f.calls)
	}

	f = &fake{respond: func(a []string) (string, error) {
		switch {
		case is(a, "login"):
			return "", errors.New("aws: error: argument command: Invalid choice, valid choices are: …")
		case is(a, "configure", "list-profiles"):
			return "plain\nsso-dev\n", nil
		case is(a, "configure", "get", "sso_session") && a[len(a)-1] == "sso-dev":
			return "my-sso\n", nil
		}
		return "", nil
	}}
	if err := newTest(f, nil).Login(context.Background(), events.Discard); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1]
	if !reflect.DeepEqual(last, []string{"sso", "login", "--profile", "sso-dev"}) {
		t.Fatalf("last call %v", last)
	}

	f = &fake{respond: func(a []string) (string, error) {
		if is(a, "login") {
			return "", errors.New("Invalid choice")
		}
		return "", nil
	}}
	if err := newTest(f, nil).Login(context.Background(), events.Discard); err == nil || !strings.Contains(err.Error(), "aws configure") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeyPairNameStable(t *testing.T) {
	a, b := KeyPairName("ssh-ed25519 AAA x\n"), KeyPairName("ssh-ed25519 AAA x")
	if a != b || !strings.HasPrefix(a, "skybuild-") || len(a) != len("skybuild-")+8 {
		t.Fatalf("%s %s", a, b)
	}
}

func dump(calls [][]string) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString("  " + strings.Join(c, " ") + "\n")
	}
	return b.String()
}
