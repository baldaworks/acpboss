package provider

import (
	"context"
	"testing"
	"time"
)

func TestModelMetadataAndSelectionOnWire(t *testing.T) {
	for _, mode := range []string{"", "legacy", "grouped", "no-models"} {
		t.Run(mode, func(t *testing.T) {
			s := settings(t)
			p, err := Start(t.Context(), fixture(t, "alpha", mode), s, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { ctx, c := context.WithTimeout(context.Background(), time.Second); defer c(); _ = p.Close(ctx) }()
			m, err := p.NewSession(t.Context(), s.WorkingDir, nil)
			if mode == "no-models" {
				if err == nil {
					t.Fatal("missing catalog accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.Current != "org/model" || len(m.Models) != 2 || m.Raw["_meta"] == nil {
				t.Fatal(m)
			}
			m, err = p.SelectModel(t.Context(), m, "other")
			if err != nil || m.Current != "other" {
				t.Fatal(m, err)
			}
			if _, err := p.SelectModel(t.Context(), m, "alpha/other"); err == nil {
				t.Fatal("qualified ID passed upstream")
			}
		})
	}
}
