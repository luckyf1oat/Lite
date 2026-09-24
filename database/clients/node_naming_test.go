package clients

import (
	"testing"

	"github.com/nuomiiiii/lite/database/models"
)

type fakeNamer struct {
	proposal NameProposal
	proposed []string
	submits  []string
}

func (f *fakeNamer) Propose(uuid, currentName, ip string) NameProposal {
	f.proposed = append(f.proposed, uuid+"|"+currentName+"|"+ip)
	return f.proposal
}

func (f *fakeNamer) Submit(uuid, ip string) {
	f.submits = append(f.submits, uuid+"|"+ip)
}

func (f *fakeNamer) Forget(string) {}

func withFakeNamer(t *testing.T, namer *fakeNamer) {
	t.Helper()
	previous := CurrentNodeNamer()
	SetNodeNamer(namer)
	t.Cleanup(func() { SetNodeNamer(previous) })
}

func TestProposeNodeNamePersistsCachedCandidate(t *testing.T) {
	namer := &fakeNamer{proposal: NameProposal{Candidate: "CN-203.0.113.7-AS4837-China Unicom"}}
	withFakeNamer(t, namer)

	update := map[string]any{"uuid": "node-a"}
	proposeNodeName(update, models.Client{UUID: "node-a", Name: "client_1234abcd", IPv4: "203.0.113.7"})

	if update["name"] != "CN-203.0.113.7-AS4837-China Unicom" {
		t.Fatalf("name = %v", update["name"])
	}
	if update["name_auto_generated"] != true {
		t.Fatalf("name_auto_generated = %v, want true", update["name_auto_generated"])
	}
	if len(namer.submits) != 0 {
		t.Fatalf("submits = %v, want none for a cached candidate", namer.submits)
	}
}

func TestProposeNodeNameQueuesLookupWithoutBlockingUpdate(t *testing.T) {
	namer := &fakeNamer{proposal: NameProposal{NeedsLookup: true}}
	withFakeNamer(t, namer)

	update := map[string]any{"uuid": "node-b"}
	proposeNodeName(update, models.Client{UUID: "node-b", Name: "client_bbbbbbbb", IPv6: "2001:db8::1"})

	if _, exists := update["name"]; exists {
		t.Fatalf("name must not be rewritten before the lookup, got %v", update["name"])
	}
	if len(namer.submits) != 1 || namer.submits[0] != "node-b|2001:db8::1" {
		t.Fatalf("submits = %v, want the IPv6 fallback", namer.submits)
	}
}

func TestProposeNodeNameRespectsExplicitName(t *testing.T) {
	namer := &fakeNamer{proposal: NameProposal{Candidate: "generated"}}
	withFakeNamer(t, namer)

	update := map[string]any{"uuid": "node-c", "name": "Administrator choice"}
	proposeNodeName(update, models.Client{UUID: "node-c", Name: "client_cccccccc", IPv4: "203.0.113.9"})

	if update["name"] != "Administrator choice" {
		t.Fatalf("name = %v, want the explicit value", update["name"])
	}
	if len(namer.proposed) != 0 {
		t.Fatalf("Propose was called for an explicit rename: %v", namer.proposed)
	}
}

func TestProposeNodeNameWithoutNamerIsInert(t *testing.T) {
	withFakeNamer(t, nil)

	update := map[string]any{"uuid": "node-d"}
	proposeNodeName(update, models.Client{UUID: "node-d", Name: "client_dddddddd", IPv4: "203.0.113.10"})

	if _, exists := update["name"]; exists {
		t.Fatalf("name must not be set without a namer, got %v", update["name"])
	}
}

func TestDefaultDeploymentProfileEnablesRemoteControlAndLongInterval(t *testing.T) {
	profile := defaultDeploymentProfile(models.Client{UUID: "node-e"})

	if profile.Platform != "linux" {
		t.Fatalf("platform = %q", profile.Platform)
	}
	if !profile.EnableRemoteControl {
		t.Fatal("remote control must be enabled by default for a WebSSH-only fleet")
	}
	if !profile.EnableInterval {
		t.Fatal("interval must be enabled by default")
	}
	if profile.Interval != DefaultDeploymentIntervalSeconds {
		t.Fatalf("interval = %v, want %v", profile.Interval, DefaultDeploymentIntervalSeconds)
	}
	if DefaultDeploymentIntervalSeconds != 600 {
		t.Fatalf("default interval = %v, want 600 seconds", DefaultDeploymentIntervalSeconds)
	}
}
