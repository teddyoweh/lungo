package azure

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

func newTest(f *fake) *Azure {
	return &Azure{
		run: f.run,
		has: func(string) bool { return true },
		tempFile: func(pattern, _ string) (string, func(), error) {
			if strings.Contains(pattern, "key") {
				return "/tmp/sky-key.pub", func() {}, nil
			}
			return "/tmp/sky-setup.sh", func() {}, nil
		},
	}
}

const sub = "00000000-1111-2222-3333-444444444444"

func j(args ...string) []string {
	return append(args, "--only-show-errors", "-o", "json", "--subscription", sub)
}

var notFound = errors.New("(ResourceNotFound) The Resource 'Microsoft.Compute/disks/api-box-data' under resource group 'skybuild-eastus' was not found.")

func spec() model.Spec {
	return model.Spec{Name: "api-box", Provider: "azure", Account: sub, Region: "eastus", Size: "Standard_D4s_v5",
		DiskGB: 100, User: "teddy", PublicKey: "ssh-ed25519 AAAAC3Nza skybuild@mac", Bootstrap: "#!/bin/bash"}
}

const vmCreateOut = `{"fqdns":"","id":"/subscriptions/x/resourceGroups/skybuild-eastus/providers/Microsoft.Compute/virtualMachines/api-box",
 "location":"eastus","macAddress":"00-0D-3A-00-00-00","powerState":"VM running","privateIpAddress":"10.0.0.4",
 "publicIpAddress":"20.1.2.3","resourceGroup":"skybuild-eastus","zones":""}`

func TestCreateNew(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		switch {
		case is(a, "disk", "show"):
			return "", notFound
		case is(a, "vm", "create"):
			return vmCreateOut, nil
		}
		return "{}", nil
	}}
	m, err := newTest(f).Create(context.Background(), spec(), events.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		j("group", "create", "--name", "skybuild-eastus", "--location", "eastus", "--tags", "skybuild=machine"),
		j("disk", "show", "--resource-group", "skybuild-eastus", "--name", "api-box-data"),
		j("disk", "create", "--resource-group", "skybuild-eastus", "--name", "api-box-data", "--location", "eastus",
			"--size-gb", "100", "--sku", "StandardSSD_LRS", "--tags", "skybuild=machine", "sky-user=teddy", "skybuild-volume=api-box"),
		j("vm", "create", "--resource-group", "skybuild-eastus", "--name", "api-box", "--location", "eastus",
			"--image", "Canonical:ubuntu-24_04-lts:server:latest", "--size", "Standard_D4s_v5",
			"--admin-username", "teddy", "--ssh-key-values", "/tmp/sky-key.pub",
			"--custom-data", "/tmp/sky-setup.sh",
			"--attach-data-disks", "api-box-data",
			"--os-disk-size-gb", "30", "--storage-sku", "StandardSSD_LRS",
			"--public-ip-sku", "Standard", "--nsg-rule", "SSH",
			"--os-disk-delete-option", "Delete", "--nic-delete-option", "Delete", "--data-disk-delete-option", "Detach",
			"--tags", "skybuild=machine", "sky-user=teddy"),
		j("network", "nsg", "rule", "create", "--resource-group", "skybuild-eastus", "--nsg-name", "api-boxNSG",
			"--name", "tailscale", "--priority", "1010", "--direction", "Inbound", "--access", "Allow", "--protocol", "Udp",
			"--destination-port-ranges", "41641", "--source-address-prefixes", "*"),
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", dump(f.calls), dump(want))
	}
	if m.PublicIP != "20.1.2.3" || m.Status != model.StatusRunning || m.VolumeID != "api-box-data" ||
		m.Extra["resourceGroup"] != "skybuild-eastus" || m.DiskGB != 100 || m.InstanceID != "api-box" {
		t.Fatalf("machine: %+v", m)
	}
}

func TestCreateReusesDisk(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		switch {
		case is(a, "disk", "show"):
			return `{"name":"api-box-data","diskSizeGb":256,"diskState":"Unattached","tags":{"skybuild":"machine"}}`, nil
		case is(a, "vm", "create"):
			return vmCreateOut, nil
		}
		return "{}", nil
	}}
	rec := &events.Recorder{}
	m, err := newTest(f).Create(context.Background(), spec(), rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if is(c, "disk", "create") {
			t.Fatal("created a disk while one exists")
		}
	}
	if m.DiskGB != 256 {
		t.Fatalf("disk %d", m.DiskGB)
	}
	found := false
	for _, e := range rec.Events {
		found = found || strings.Contains(e.Message, "Reusing the existing api-box-data")
	}
	if !found {
		t.Fatal("no reuse message")
	}
}

func TestCreateRefusesAttachedDisk(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		if is(a, "disk", "show") {
			return `{"name":"api-box-data","diskSizeGb":256,"diskState":"Attached"}`, nil
		}
		return "{}", nil
	}}
	_, err := newTest(f).Create(context.Background(), spec(), events.Discard)
	if err == nil || !strings.Contains(err.Error(), "attached") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateNSGFailureIsAWarning(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		switch {
		case is(a, "disk", "show"):
			return "", notFound
		case is(a, "vm", "create"):
			return vmCreateOut, nil
		case is(a, "network", "nsg", "rule", "create"):
			return "", errors.New("(AuthorizationFailed)")
		}
		return "{}", nil
	}}
	if _, err := newTest(f).Create(context.Background(), spec(), events.Discard); err != nil {
		t.Fatal(err)
	}
}

func machine() *model.Machine {
	return &model.Machine{Name: "api-box", Provider: "azure", Account: sub, Region: "eastus", InstanceID: "api-box",
		VolumeID: "api-box-data", Size: "Standard_D4s_v5", DiskGB: 100, Extra: map[string]string{"resourceGroup": "skybuild-eastus"}}
}

const showRunning = `{"name":"api-box","location":"eastus","resourceGroup":"skybuild-eastus","powerState":"VM running",
 "publicIps":"20.1.2.3","hardwareProfile":{"vmSize":"Standard_D4s_v5"},"tags":{"skybuild":"machine","sky-user":"teddy"}}`

var showStopped = strings.Replace(showRunning, "VM running", "VM deallocated", 1)

func TestOperations(t *testing.T) {
	rg := []string{"--resource-group", "skybuild-eastus", "--name", "api-box"}
	show := j(append([]string{"vm", "show", "--show-details"}, rg...)...)
	cases := []struct {
		name string
		show string
		fail func(a []string) error
		do   func(z *Azure, m *model.Machine) error
		want [][]string
	}{
		{"start", showRunning, nil, func(z *Azure, m *model.Machine) error { return z.Start(context.Background(), m) }, [][]string{
			j(append([]string{"vm", "start"}, rg...)...), show,
		}},
		{"stop deallocates", showRunning, nil, func(z *Azure, m *model.Machine) error { return z.Stop(context.Background(), m) }, [][]string{
			j(append([]string{"vm", "deallocate"}, rg...)...),
		}},
		{"refresh", showRunning, nil, func(z *Azure, m *model.Machine) error { return z.Refresh(context.Background(), m) }, [][]string{show}},
		{"delete", showRunning, nil, func(z *Azure, m *model.Machine) error {
			return z.Delete(context.Background(), m, false, events.Discard)
		}, [][]string{
			j(append(append([]string{"vm", "delete"}, rg...), "--yes")...),
			j("network", "public-ip", "delete", "--resource-group", "skybuild-eastus", "--name", "api-boxPublicIP"),
			j("network", "nsg", "delete", "--resource-group", "skybuild-eastus", "--name", "api-boxNSG"),
			j("network", "vnet", "delete", "--resource-group", "skybuild-eastus", "--name", "api-boxVNET"),
			j("disk", "delete", "--resource-group", "skybuild-eastus", "--name", "api-box-data", "--yes"),
		}},
		{"delete keep disk", showRunning, nil, func(z *Azure, m *model.Machine) error { return z.Delete(context.Background(), m, true, events.Discard) }, [][]string{
			j(append(append([]string{"vm", "delete"}, rg...), "--yes")...),
			j("network", "public-ip", "delete", "--resource-group", "skybuild-eastus", "--name", "api-boxPublicIP"),
			j("network", "nsg", "delete", "--resource-group", "skybuild-eastus", "--name", "api-boxNSG"),
			j("network", "vnet", "delete", "--resource-group", "skybuild-eastus", "--name", "api-boxVNET"),
		}},
		{"resize", showRunning, nil, func(z *Azure, m *model.Machine) error {
			return z.Resize(context.Background(), m, "Standard_D8s_v5", events.Discard)
		}, [][]string{
			show,
			j(append(append([]string{"vm", "resize"}, rg...), "--size", "Standard_D8s_v5")...),
			show,
		}},
		{"resize needs deallocate", showRunning, func(a []string) error {
			if is(a, "vm", "resize") {
				return errors.New("(OperationNotAllowed) Unable to resize the VM 'api-box' because the requested size Standard_D8s_v5 is not available in the current hardware cluster.")
			}
			return nil
		}, func(z *Azure, m *model.Machine) error {
			return z.Resize(context.Background(), m, "Standard_D8s_v5", events.Discard)
		}, [][]string{
			show,
			j(append(append([]string{"vm", "resize"}, rg...), "--size", "Standard_D8s_v5")...),
			j(append([]string{"vm", "deallocate"}, rg...)...),
			j(append(append([]string{"vm", "resize"}, rg...), "--size", "Standard_D8s_v5")...),
			j(append([]string{"vm", "start"}, rg...)...),
			show,
		}},
		{"grow disk running", showRunning, nil, func(z *Azure, m *model.Machine) error { return z.GrowDisk(context.Background(), m, 300) }, [][]string{
			show,
			j(append([]string{"vm", "deallocate"}, rg...)...),
			j("disk", "update", "--resource-group", "skybuild-eastus", "--name", "api-box-data", "--size-gb", "300"),
			j(append([]string{"vm", "start"}, rg...)...),
			show,
		}},
		{"grow disk stopped", showStopped, nil, func(z *Azure, m *model.Machine) error { return z.GrowDisk(context.Background(), m, 300) }, [][]string{
			show,
			j("disk", "update", "--resource-group", "skybuild-eastus", "--name", "api-box-data", "--size-gb", "300"),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			failed := false
			f := &fake{respond: func(a []string) (string, error) {
				if c.fail != nil && !failed {
					if err := c.fail(a); err != nil {
						failed = true
						return "", err
					}
				}
				if is(a, "vm", "show") {
					return c.show, nil
				}
				return "", nil
			}}
			m := machine()
			if err := c.do(newTest(f), m); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.calls, c.want) {
				t.Fatalf("calls:\n%s\nwant:\n%s", dump(f.calls), dump(c.want))
			}
		})
	}
}

func TestRefreshParses(t *testing.T) {
	f := &fake{respond: func([]string) (string, error) {
		return strings.Replace(showRunning, `"publicIps":"20.1.2.3"`, `"publicIps":"20.1.2.3,20.9.9.9"`, 1), nil
	}}
	m := machine()
	m.Size = ""
	if err := newTest(f).Refresh(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if m.Status != model.StatusRunning || m.PublicIP != "20.1.2.3" || m.Size != "Standard_D4s_v5" {
		t.Fatalf("%+v", m)
	}
	f = &fake{respond: func([]string) (string, error) {
		return "", errors.New("(ResourceNotFound) The Resource 'Microsoft.Compute/virtualMachines/api-box' under resource group 'skybuild-eastus' was not found.")
	}}
	if err := newTest(f).Refresh(context.Background(), m); err != nil || m.Status != model.StatusMissing {
		t.Fatalf("%v %s", err, m.Status)
	}
}

func TestResourceGroupFallback(t *testing.T) {
	m := machine()
	m.Extra = nil
	m.Region = "westus3"
	if rgOf(m) != "skybuild-westus3" {
		t.Fatal(rgOf(m))
	}
}

func TestMapPower(t *testing.T) {
	for in, want := range map[string]string{
		"VM running": model.StatusRunning, "VM starting": model.StatusStarting, "VM stopping": model.StatusStopping,
		"VM deallocating": model.StatusStopping, "VM stopped": model.StatusStopped, "VM deallocated": model.StatusStopped,
		"": model.StatusUnknown,
	} {
		if got := mapPower(in); got != want {
			t.Errorf("%q → %s, want %s", in, got, want)
		}
	}
}

func TestDiscover(t *testing.T) {
	f := &fake{respond: func([]string) (string, error) {
		return `[{"name":"api-box","location":"eastus","resourceGroup":"SKYBUILD-EASTUS","powerState":"VM deallocated",
		  "publicIps":"20.1.2.3","timeCreated":"2026-10-03T12:00:00.1234567+00:00",
		  "hardwareProfile":{"vmSize":"Standard_D4s_v5"},"tags":{"skybuild":"machine","sky-user":"teddy"},
		  "storageProfile":{"dataDisks":[{"name":"api-box-data","lun":0,"diskSizeGb":128}]}}]`, nil
	}}
	got, err := newTest(f).Discover(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{j("vm", "list", "--show-details", "--query", "[?tags.skybuild=='machine']")}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v", f.calls)
	}
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	m := got[0]
	if m.Name != "api-box" || m.Status != model.StatusStopped || m.User != "teddy" || m.VolumeID != "api-box-data" ||
		m.DiskGB != 128 || m.Extra["resourceGroup"] != "SKYBUILD-EASTUS" || m.CreatedAt.IsZero() || m.Account != sub {
		t.Fatalf("%+v", m)
	}
}

func TestStatus(t *testing.T) {
	f := &fake{respond: func(a []string) (string, error) {
		if !reflect.DeepEqual(a, []string{"account", "list", "--only-show-errors", "-o", "json"}) {
			t.Errorf("unexpected call %v", a)
		}
		return `[
		 {"id":"sub-a","name":"Dev","isDefault":false,"state":"Enabled","user":{"name":"teddy@example.com","type":"user"}},
		 {"id":"sub-b","name":"Prod","isDefault":true,"state":"Enabled","user":{"name":"teddy@example.com","type":"user"}},
		 {"id":"sub-c","name":"Old","isDefault":false,"state":"Disabled","user":{"name":"teddy@example.com","type":"user"}}]`, nil
	}}
	st := newTest(f).Status(context.Background())
	if !st.LoggedIn || st.Identity != "teddy@example.com" || len(st.Accounts) != 2 || !st.Accounts[1].Default || st.Accounts[0].Label != "Dev" {
		t.Fatalf("%+v", st)
	}
	f = &fake{respond: func([]string) (string, error) { return "[]", nil }}
	if st := newTest(f).Status(context.Background()); st.LoggedIn || st.Hint == "" {
		t.Fatalf("%+v", st)
	}
	z := newTest(&fake{})
	z.has = func(string) bool { return false }
	if st := z.Status(context.Background()); st.Installed {
		t.Fatalf("%+v", st)
	}
}

func TestLogin(t *testing.T) {
	f := &fake{}
	if err := newTest(f).Login(context.Background(), events.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, [][]string{{"login", "--only-show-errors", "-o", "none"}}) {
		t.Fatalf("%v", f.calls)
	}
}

func dump(calls [][]string) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString("  " + strings.Join(c, " ") + "\n")
	}
	return b.String()
}
