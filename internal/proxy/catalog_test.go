package proxy

import (
	"context"
	"github.com/baldaworks/acpboss/internal/provider"
	"github.com/baldaworks/acpboss/internal/testprovider"
	"testing"
	"time"
)

func TestACPFixtureProcess(t *testing.T) { testprovider.MaybeRun() }

func TestCatalogRemovesStoppedProvider(t *testing.T) {
	c := testprovider.Config(t, map[string]string{"alpha": ""})
	resolved, err := c.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	p, err := provider.Start(t.Context(), resolved[0], c.Settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := p.NewSession(t.Context(), c.Settings.WorkingDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(AvailableModels(p, m)) != 2 {
		t.Fatal("missing live models")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(AvailableModels(p, m)) != 0 {
		t.Fatal("dead models advertised")
	}
}

func TestQualifiedModelIdentity(t *testing.T) {
	m := &provider.Metadata{Models: []provider.Model{{ID: "org/model", Name: "Model"}}}
	a := QualifyModels("alpha", m)
	b := QualifyModels("beta", m)
	if a[0].ID != "alpha/org/model" || b[0].ID != "beta/org/model" {
		t.Fatal(a, b)
	}
	p, u, err := SplitModel(b[0].ID)
	if err != nil || p != "beta" || u != "org/model" {
		t.Fatal(p, u, err)
	}
	for _, id := range []string{"model", "/model", "alpha/"} {
		if _, _, err := SplitModel(id); err == nil {
			t.Fatal(id)
		}
	}
}
