package kubernetes

import (
	"testing"

	"k8s.io/client-go/rest"
)

func TestForConfigBuildsAllClients(t *testing.T) {
	f := NewClientFactory(20, 40)
	clients, err := f.ForConfig(&rest.Config{Host: "https://prod.example.com:6443"})
	if err != nil {
		t.Fatal(err)
	}
	if clients.Typed == nil || clients.Dynamic == nil || clients.Discovery == nil || clients.Mapper == nil {
		t.Fatalf("all client flavors must be built: %+v", clients)
	}
}

func TestForConfigDoesNotMutateInput(t *testing.T) {
	f := NewClientFactory(20, 40)
	cfg := &rest.Config{Host: "https://prod.example.com:6443"}
	if _, err := f.ForConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.QPS != 0 || cfg.Burst != 0 {
		t.Errorf("input config must not be mutated: qps=%v burst=%v", cfg.QPS, cfg.Burst)
	}
}

func TestForConfigIsolatesBundles(t *testing.T) {
	// Two bundles from different clusters must share nothing: this is the
	// client-level half of the no-global-state rule (spec §4).
	f := NewClientFactory(20, 40)
	prod, err := f.ForConfig(&rest.Config{Host: "https://prod.example.com:6443"})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := f.ForConfig(&rest.Config{Host: "https://dev.example.com:6443"})
	if err != nil {
		t.Fatal(err)
	}
	prodHost := prod.Typed.Discovery().RESTClient().Get().URL().Host
	devHost := dev.Typed.Discovery().RESTClient().Get().URL().Host
	if prodHost == devHost {
		t.Errorf("bundles share a host: %q", prodHost)
	}
}

func TestForConfigNil(t *testing.T) {
	f := NewClientFactory(20, 40)
	if _, err := f.ForConfig(nil); err == nil {
		t.Fatal("expected error for nil config")
	}
}
